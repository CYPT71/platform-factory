// pf license issues, verifies, and inspects commercial license files - the
// CLI facade over internal/license, mirroring verify-release's own
// --trusted-key/--key-dir trust model (an envelope's claimed key ID is
// never itself trusted; the caller pins which key to accept). See
// docs/licensing.md for what a license grants and docs/legal/pricing.md
// for what, if anything, is actually sold today - this command only
// provides the primitive; nothing in this codebase verifies a license to
// gate a capability yet.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/CYPT71/platform-factory/internal/attestation"
	"github.com/CYPT71/platform-factory/internal/license"
	"github.com/CYPT71/platform-factory/internal/signing"
)

func runLicense(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: platform-factory license [issue|verify|show] [OPTIONS]")
		return 2
	}
	switch args[0] {
	case "issue":
		return runLicenseIssue(args[1:], stdout, stderr)
	case "verify":
		return runLicenseVerify(args[1:], stdout, stderr)
	case "show":
		return runLicenseShow(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "platform-factory license: unknown action %q; want issue, verify, or show\n", args[0])
		return 2
	}
}

func runLicenseIssue(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("license issue", flag.ContinueOnError)
	flags.SetOutput(stderr)
	keyDir := flags.String("key-dir", "", "signing.FileKeyStore directory holding the issuer's private key (required)")
	keyName := flags.String("key-name", "vendor", "key name to load from --key-dir")
	licenseID := flags.String("license-id", "", "stable identifier for this license (required)")
	licensee := flags.String("licensee", "", "who this license is issued to (required)")
	edition := flags.String("edition", "", "commercial offering this license grants, e.g. enterprise (required)")
	seats := flags.Int("seats", 0, "licensed seat/instance count; 0 means unlimited")
	var features stringList
	flags.Var(&features, "feature", "gated capability this license unlocks; repeatable")
	expiresIn := flags.Duration("expires-in", 0, "expiry relative to now, e.g. 8760h for one year; 0 means no expiry")
	out := flags.String("out", "", "output file for the signed license envelope (required)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *keyDir == "" || *licenseID == "" || *licensee == "" || *edition == "" || *out == "" {
		fmt.Fprintln(stderr, "platform-factory license issue: --key-dir, --license-id, --licensee, --edition and --out are required")
		return 2
	}
	store, err := signing.NewFileKeyStore(*keyDir)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory license issue: %v\n", err)
		return 1
	}
	publicKey, err := store.PublicKey(*keyName)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory license issue: %v\n", err)
		return 1
	}
	keyID := "ed25519:" + base64.RawURLEncoding.EncodeToString(publicKey)

	issuedAt := time.Now().UTC()
	lic := license.License{
		LicenseID: *licenseID, Licensee: *licensee, Edition: *edition,
		Seats: *seats, Features: features, IssuedAt: issuedAt,
	}
	if *expiresIn > 0 {
		lic.ExpiresAt = issuedAt.Add(*expiresIn)
	}
	envelope, err := license.Issue(store, *keyName, keyID, lic)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory license issue: %v\n", err)
		return 1
	}
	encoded, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory license issue: encode envelope: %v\n", err)
		return 1
	}
	if err := os.WriteFile(*out, encoded, 0o644); err != nil {
		fmt.Fprintf(stderr, "platform-factory license issue: write %s: %v\n", *out, err)
		return 1
	}
	fmt.Fprintf(stdout, "issued %s for %s (key %s) -> %s\n", *licenseID, *licensee, keyID, *out)
	return 0
}

func runLicenseVerify(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("license verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var trustedKeyFlags stringList
	flags.Var(&trustedKeyFlags, "trusted-key", "pinned signer, ed25519:BASE64URL; repeatable")
	keyDir := flags.String("key-dir", "", "load one more trusted key from a local signing.FileKeyStore directory")
	keyName := flags.String("key-name", "vendor", "key name to load from --key-dir")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: platform-factory license verify [OPTIONS] LICENSE_FILE")
		return 2
	}
	trusted, err := loadLicenseTrustedKeys(trustedKeyFlags, *keyDir, *keyName)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory license verify: %v\n", err)
		return 2
	}
	envelope, err := readLicenseEnvelope(flags.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory license verify: %v\n", err)
		return 1
	}
	lic, err := license.Verify(envelope, trusted, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory license verify: %v\n", err)
		return 1
	}
	encoded, _ := json.MarshalIndent(lic, "", "  ")
	fmt.Fprintln(stdout, string(encoded))
	return 0
}

func runLicenseShow(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("license show", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: platform-factory license show LICENSE_FILE")
		return 2
	}
	envelope, err := readLicenseEnvelope(flags.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory license show: %v\n", err)
		return 1
	}
	payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory license show: invalid payload encoding\n")
		return 1
	}
	var lic license.License
	if err := json.Unmarshal(payload, &lic); err != nil {
		fmt.Fprintf(stderr, "platform-factory license show: decode predicate: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "note: show does not verify the signature; use `license verify` before trusting this content")
	encoded, _ := json.MarshalIndent(lic, "", "  ")
	fmt.Fprintln(stdout, string(encoded))
	for _, signature := range envelope.Signatures {
		fmt.Fprintf(stdout, "signed by: %s\n", signature.KeyID)
	}
	return 0
}

func readLicenseEnvelope(path string) (attestation.Envelope, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return attestation.Envelope{}, err
	}
	var envelope attestation.Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return attestation.Envelope{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return envelope, nil
}

// loadLicenseTrustedKeys mirrors runVerifyRelease's own trust model: an
// envelope's claimed key ID is never itself trusted, the caller pins
// which key(s) to accept via --trusted-key and/or --key-dir.
func loadLicenseTrustedKeys(flagged []string, keyDir, keyName string) (map[string]ed25519.PublicKey, error) {
	keys := map[string]ed25519.PublicKey{}
	for _, raw := range flagged {
		algo, encoded, found := strings.Cut(raw, ":")
		if !found || algo != "ed25519" {
			return nil, fmt.Errorf("--trusted-key %q must have the form ed25519:BASE64URL", raw)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("--trusted-key %q is not a valid ed25519 public key", raw)
		}
		keys[raw] = ed25519.PublicKey(decoded)
	}
	if keyDir != "" {
		store, err := signing.NewFileKeyStore(keyDir)
		if err != nil {
			return nil, err
		}
		publicKey, err := store.PublicKey(keyName)
		if err != nil {
			return nil, err
		}
		keys["ed25519:"+base64.RawURLEncoding.EncodeToString(publicKey)] = publicKey
	}
	if len(keys) == 0 {
		return nil, errors.New("at least one of --trusted-key or --key-dir is required")
	}
	return keys, nil
}
