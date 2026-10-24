package plugin

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	hostplugin "github.com/CYPT71/platform-factory/internal/plugin"
	"github.com/CYPT71/platform-factory/internal/signing"
)

// BundleOptions describes a publisher operation. Store holds the private key;
// only its public key is exported. No plugin code is executed during packaging.
type BundleOptions struct {
	Source, Executable, Output, KeyName string
	Store                               signing.KeyStore
}

// BuildBundle pins the bytes actually copied, signs the complete manifest and
// publishes an installable directory. Existing outputs are never replaced.
func BuildBundle(options BundleOptions) (hostplugin.Manifest, error) {
	var empty hostplugin.Manifest
	if options.Source == "" || options.Executable == "" || options.Output == "" || options.Store == nil || options.KeyName == "" {
		return empty, errors.New("plugin bundle: source, executable, output and signing key are required")
	}
	manifest, err := hostplugin.LoadManifest(options.Source)
	if err != nil {
		return empty, err
	}
	// Output is portable across supported hosts, including Windows. A basename
	// also avoids collisions with the manifest and public-key export.
	const pendingManifest = ".plugin-manifest.pending"
	name := filepath.Base(options.Executable)
	if name == "." || strings.EqualFold(name, "plugin.json") || strings.EqualFold(name, "publisher.pem") || strings.EqualFold(name, pendingManifest) || strings.ContainsAny(name, "\\:\x00") {
		return empty, errors.New("plugin bundle: invalid executable name")
	}
	info, err := os.Lstat(options.Executable)
	if err != nil {
		return empty, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return empty, errors.New("plugin bundle: executable must be a non-empty regular file without symlinks")
	}
	output, err := filepath.Abs(options.Output)
	if err != nil {
		return empty, err
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return empty, err
		}
		return empty, fmt.Errorf("plugin bundle: refusing to overwrite %s", output)
	}
	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return empty, err
	}
	temporary, err := os.MkdirTemp(parent, ".bundle-*")
	if err != nil {
		return empty, err
	}
	defer os.RemoveAll(temporary)
	source, err := os.Open(options.Executable)
	if err != nil {
		return empty, err
	}
	defer source.Close()
	opened, err := source.Stat()
	if err != nil {
		return empty, err
	}
	if !os.SameFile(info, opened) {
		return empty, errors.New("plugin bundle: executable changed before copying")
	}
	target, err := os.OpenFile(filepath.Join(temporary, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return empty, err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(target, hash), source)
	closeErr := target.Close()
	if copyErr != nil {
		return empty, copyErr
	}
	if closeErr != nil {
		return empty, closeErr
	}
	if size == 0 {
		return empty, errors.New("plugin bundle: executable became empty")
	}
	manifest.Executable = name
	manifest.Digest = "sha256:" + hex.EncodeToString(hash.Sum(nil))
	manifest.Signature = nil
	if err := manifest.Validate(); err != nil {
		return empty, err
	}
	public, err := options.Store.PublicKey(options.KeyName)
	if err != nil {
		return empty, err
	}
	if len(public) != ed25519.PublicKeySize {
		return empty, errors.New("plugin bundle: invalid publisher public key")
	}
	payload, err := manifest.SigningBytes()
	if err != nil {
		return empty, err
	}
	signature, err := options.Store.Sign(options.KeyName, payload)
	if err != nil {
		return empty, err
	}
	manifest.Signature = &hostplugin.ManifestSignature{Algorithm: "ed25519", KeyID: options.KeyName, Value: base64.StdEncoding.EncodeToString(signature)}
	if err := manifest.Validate(); err != nil {
		return empty, err
	}
	if err := manifest.VerifySignature([]ed25519.PublicKey{public}); err != nil {
		return empty, err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return empty, err
	}
	if err := os.WriteFile(filepath.Join(temporary, pendingManifest), append(encoded, '\n'), 0600); err != nil {
		return empty, err
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return empty, err
	}
	if err := os.WriteFile(filepath.Join(temporary, "publisher.pem"), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0644); err != nil {
		return empty, err
	}
	if err := manifest.VerifyExecutable(temporary); err != nil {
		return empty, err
	}
	// Reserve exclusively, then publish the manifest last. Discovery ignores
	// the directory until all executable/key bytes have been installed. This
	// works on Windows too, where directory replacement is not portable.
	if err := os.Mkdir(output, 0700); err != nil {
		return empty, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(output)
		}
	}()
	for _, files := range [][2]string{{name, name}, {"publisher.pem", "publisher.pem"}, {pendingManifest, hostplugin.ManifestFileName}} {
		if err := os.Rename(filepath.Join(temporary, files[0]), filepath.Join(output, files[1])); err != nil {
			return empty, err
		}
	}
	published = true
	return manifest, nil
}

// VerifyBundle authenticates metadata and executable bytes without executing
// the plugin or mutating the managed installation directory.
func VerifyBundle(source string, keys []ed25519.PublicKey, allowUnsigned bool) (hostplugin.Manifest, error) {
	manifest, err := hostplugin.LoadManifest(source)
	if err != nil {
		return manifest, err
	}
	if err := manifest.VerifyExecutable(source); err != nil {
		return manifest, err
	}
	if manifest.Signature == nil {
		if !allowUnsigned {
			return manifest, errors.New("unsigned manifest; pass --allow-unsigned only for local development")
		}
	} else if err := manifest.VerifySignature(keys); err != nil {
		return manifest, err
	}
	return manifest, nil
}
