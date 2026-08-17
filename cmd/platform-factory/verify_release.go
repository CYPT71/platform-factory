package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/CYPT71/platform-factory/internal/app/verify"
)

// releaseVerification is kept as an alias so existing tests and any
// external tooling that decoded `platform-factory verify-release --json`
// output into this type name keep compiling - the real type now lives
// in internal/app/verify as VerificationResult.
type releaseVerification = verify.VerificationResult

// runVerifyRelease is the CLI facade over internal/app/verify.Service -
// service, formats the result, and picks an exit code. Every actual
// verification step - layout, signature, provenance, SBOM, policy -
// lives in the service, where it's tested without going through the CLI
// at all.
//
// Trust is never inferred from an envelope's own claimed key ID: the
// caller must pin at least one expected public key via --trusted-key or
// --key-dir/--key-name, matching this project's threat model (T10 in
// Threat-Model-and-Residual-Risks.md) that verification must be against
// a specific pinned key, not an open trust root.
func runVerifyRelease(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("verify-release", flag.ContinueOnError)
	flags.SetOutput(stderr)
	signatureFile := flags.String("signature", "", "DSSE envelope JSON for the published subject signature")
	provenanceFile := flags.String("provenance", "", "provenance predicate JSON, signed (DSSE envelope) or raw")
	sbomFile := flags.String("sbom", "", "SBOM document JSON")
	policyFile := flags.String("policy", "", "native publication policy JSON")
	evidenceFile := flags.String("evidence", "", "verified build evidence JSON evaluated by --policy")
	var trustedKeyFlags stringList
	flags.Var(&trustedKeyFlags, "trusted-key", "pinned signer, ed25519:BASE64URL; repeatable")
	keyDir := flags.String("key-dir", "", "load one more trusted key from a local signing.FileKeyStore directory")
	keyName := flags.String("key-name", "release", "key name to load from --key-dir")
	sourceReference := flags.String("source-ref", "", "reference to select from a multi-image layout")
	allowIncomplete := flags.Bool("allow-incomplete-evidence", false, "development escape hatch: permit verification without the complete evidence set")
	outputFormat := flags.String("format", "json", "result format: json or text")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: platform-factory verify-release [OPTIONS] LAYOUT")
		return 2
	}
	if !validOutputFormat(*outputFormat) {
		fmt.Fprintln(stderr, "platform-factory verify-release: format must be json or text")
		return 2
	}
	if !*allowIncomplete && (*signatureFile == "" || *sbomFile == "" || *provenanceFile == "" ||
		*policyFile == "" || *evidenceFile == "") {
		fmt.Fprintln(stderr, "platform-factory verify-release: complete verification requires --signature, --sbom, "+
			"--provenance, --policy and --evidence (or explicitly use --allow-incomplete-evidence for development)")
		return 2
	}

	svc := verify.New()
	result, err := svc.Verify(verify.VerifyOptions{
		LayoutPath:      flags.Arg(0),
		SourceReference: *sourceReference,
		SignatureFile:   *signatureFile,
		ProvenanceFile:  *provenanceFile,
		SBOMFile:        *sbomFile,
		PolicyFile:      *policyFile,
		EvidenceFile:    *evidenceFile,
		TrustedKeyFlags: trustedKeyFlags,
		KeyDir:          *keyDir,
		KeyName:         *keyName,
	})
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory verify-release: %v\n", err)
		if errors.Is(err, verify.ErrInvalidArguments) {
			return 2
		}
		return 1
	}

	if *outputFormat == "text" {
		printReleaseVerificationText(stdout, result)
	} else {
		encoded, _ := json.MarshalIndent(struct {
			APIVersion string `json:"api_version"`
			verify.VerificationResult
		}{APIVersion: cliOutputAPIVersion, VerificationResult: result}, "", "  ")
		fmt.Fprintln(stdout, string(encoded))
	}
	if !result.Valid {
		return 1
	}
	return 0
}

func printReleaseVerificationText(w io.Writer, r releaseVerification) {
	fmt.Fprintf(w, "layout\tvalid\t%s\n", r.Digest)
	status := func(ok bool, errMsg string) string {
		if errMsg != "" {
			return "FAIL: " + errMsg
		}
		if ok {
			return "ok"
		}
		return "skipped"
	}
	fmt.Fprintf(w, "signature\t%s\n", status(r.SignatureValid, r.SignatureError))
	fmt.Fprintf(w, "provenance\t%s\n", status(r.ProvenanceValid, r.ProvenanceError))
	fmt.Fprintf(w, "sbom\t%s\n", status(r.SBOMValid, r.SBOMError))
	if r.PolicyDecision != nil {
		fmt.Fprintf(w, "policy\tallowed=%v\t%s\n", r.PolicyDecision.Allowed, strings.Join(r.PolicyDecision.Reasons, "; "))
	} else if r.PolicyError != "" {
		fmt.Fprintf(w, "policy\tFAIL: %s\n", r.PolicyError)
	} else {
		fmt.Fprintln(w, "policy\tskipped")
	}
	fmt.Fprintf(w, "release\tvalid=%v\n", r.Valid)
}

// stringList collects repeated -flag values.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}
