package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	pluginapp "github.com/CYPT71/platform-factory/internal/app/plugin"
	hostplugin "github.com/CYPT71/platform-factory/internal/plugin"
	"github.com/CYPT71/platform-factory/internal/signing"
)

func runPluginBundle(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("plugin bundle", flag.ContinueOnError)
	flags.SetOutput(stderr)
	from := flags.String("from", "", "source directory containing plugin.json template")
	executable := flags.String("executable", "", "prebuilt plugin executable")
	output := flags.String("output", "", "new bundle directory; never overwritten")
	keyDir := flags.String("key-dir", "", "private publisher key directory, outside the bundle (required)")
	keyName := flags.String("key-name", "plugin-release", "persistent Ed25519 key name; generated on first use")
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *from == "" || *executable == "" || *output == "" || *keyDir == "" || !validOutputFormat(*format) {
		fmt.Fprintln(stderr, "usage: platform-factory plugin bundle --from DIR --executable FILE --output DIR --key-dir DIR [--key-name NAME] [--format text|json]")
		return 2
	}
	// Resolve symlinks in existing ancestors before creating a private key.
	// Keys must never be created in the distributable bundle directory.
	outputPath, err := resolvedBundlePath(*output)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	keyPath, err := resolvedBundlePath(*keyDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	relative, err := filepath.Rel(outputPath, keyPath)
	if err != nil || relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		fmt.Fprintln(stderr, "plugin bundle: private key directory must be outside the bundle")
		return 2
	}
	store, err := signing.NewFileKeyStore(keyPath)
	if err != nil {
		fmt.Fprintln(stderr, "plugin bundle:", err)
		return 1
	}
	manifest, err := pluginapp.BuildBundle(pluginapp.BundleOptions{Source: *from, Executable: *executable, Output: outputPath, KeyName: *keyName, Store: store})
	if err != nil {
		fmt.Fprintln(stderr, "plugin bundle:", err)
		return 1
	}
	return writePluginBundleResult("bundle", outputPath, manifest, stdout, stderr, *format)
}

// resolvedBundlePath resolves every existing ancestor so symlink aliases cannot
// place private keys inside the output while appearing to be elsewhere.
func resolvedBundlePath(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	ancestor := absolute
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", err
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = parent
	}
}

func runPluginVerify(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("plugin verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	from := flags.String("from", "", "bundle directory")
	format := flags.String("format", "text", "output format: text or json")
	allowUnsigned := flags.Bool("allow-unsigned", false, "allow unsigned metadata for local development; digest remains required")
	var keys repeatedFlag
	flags.Var(&keys, "key", "trusted publisher public PEM key; repeatable")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *from == "" || !validOutputFormat(*format) {
		fmt.Fprintln(stderr, "usage: platform-factory plugin verify --from DIR --key PUBLIC.pem [--format text|json]")
		return 2
	}
	manifest, err := verifyPluginBundle(*from, keys, *allowUnsigned)
	if err != nil {
		fmt.Fprintln(stderr, "plugin verify:", err)
		return 1
	}
	return writePluginBundleResult("verify", *from, manifest, stdout, stderr, *format)
}

func verifyPluginBundle(source string, keyFiles []string, allowUnsigned bool) (hostplugin.Manifest, error) {
	keys := make([]ed25519.PublicKey, 0, len(keyFiles))
	for _, filename := range keyFiles {
		key, err := hostplugin.LoadPublicKey(filename)
		if err != nil {
			return hostplugin.Manifest{}, err
		}
		keys = append(keys, key)
	}
	return pluginapp.VerifyBundle(source, keys, allowUnsigned)
}

func writePluginBundleResult(action, path string, manifest hostplugin.Manifest, stdout, stderr io.Writer, format string) int {
	result := struct {
		APIVersion           string `json:"api_version"`
		Action               string `json:"action"`
		Name                 string `json:"name"`
		Version              string `json:"version"`
		Path                 string `json:"path"`
		Digest               string `json:"digest"`
		Signed               bool   `json:"signed"`
		PublisherFingerprint string `json:"publisher_fingerprint,omitempty"`
	}{APIVersion: cliOutputAPIVersion, Action: action, Name: manifest.Name, Version: manifest.Version, Path: path, Digest: manifest.Digest, Signed: manifest.Signature != nil}
	// The fingerprint is informational; callers explicitly choose trusted keys.
	if action == "bundle" {
		if key, err := hostplugin.LoadPublicKey(filepath.Join(path, "publisher.pem")); err == nil {
			digest := sha256.Sum256(key)
			result.PublisherFingerprint = "sha256:" + hex.EncodeToString(digest[:])
		}
	}
	if format == "json" {
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else {
		fmt.Fprintf(stdout, "%s %s@%s (%s, signed=%t)\n", action, manifest.Name, manifest.Version, path, result.Signed)
	}
	return 0
}
