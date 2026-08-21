package project

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/CYPT71/platform-factory/internal/strictjson"
	"go.yaml.in/yaml/v3"
)

const CurrentLockVersion = 2

type LockedInput struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// Lock pins nondeterministic inputs observed when a project is initialized.
type Lock struct {
	Version    int           `json:"version"`
	GitCommit  string        `json:"git_commit,omitempty"`
	PlanDigest string        `json:"plan_digest,omitempty"`
	Sources    []LockedInput `json:"sources,omitempty"`
	Toolchains []LockedInput `json:"toolchains,omitempty"`
	Bases      []LockedInput `json:"bases,omitempty"`
	Kernel     *LockedInput  `json:"kernel,omitempty"`
	Initramfs  *LockedInput  `json:"initramfs,omitempty"`
}

// LoadLock strictly loads a bounded v1 project lockfile.
func LoadLock(filename string) (Lock, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return Lock{}, fmt.Errorf("open project lock: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Lock{}, errors.New("project lock must be a regular file, not a symlink")
	}
	if info.Size() > 1<<20 {
		return Lock{}, errors.New("project lock exceeds 1 MiB")
	}
	file, err := os.Open(filename)
	if err != nil {
		return Lock{}, fmt.Errorf("open project lock: %w", err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return Lock{}, fmt.Errorf("read project lock: %w", err)
	}
	if len(raw) > 1<<20 {
		return Lock{}, errors.New("project lock exceeds 1 MiB")
	}
	var lock Lock
	if err := strictjson.Decode(raw, &lock); err != nil {
		return Lock{}, fmt.Errorf("decode project lock: %w", err)
	}
	if err := lock.Validate(); err != nil {
		return Lock{}, err
	}
	return lock, nil
}

// Validate rejects schemas or pins this build cannot interpret safely.
func (lock Lock) Validate() error {
	if lock.Version > CurrentLockVersion {
		return fmt.Errorf("lock version %d is newer than this platform-factory supports (max %d); upgrade platform-factory", lock.Version, CurrentLockVersion)
	}
	if lock.Version != 1 && lock.Version != CurrentLockVersion {
		return fmt.Errorf("unsupported project lock version %d", lock.Version)
	}
	if lock.GitCommit != "" && (len(lock.GitCommit) > 128 || strings.ContainsAny(lock.GitCommit, " \t\r\n\x00")) {
		return errors.New("project lock git_commit must be a single NUL-free token of at most 128 bytes")
	}
	if lock.Version == 1 {
		if lock.PlanDigest != "" || len(lock.Sources)+len(lock.Toolchains)+len(lock.Bases) != 0 || lock.Kernel != nil || lock.Initramfs != nil {
			return errors.New("project lock v1 cannot contain v2 pin fields")
		}
		return nil
	}
	if !validSHA256Digest(lock.PlanDigest) {
		return errors.New("project lock v2 plan_digest must be sha256:<64 lowercase hex>")
	}
	seen := map[string]bool{}
	for category, inputs := range map[string][]LockedInput{"source": lock.Sources, "toolchain": lock.Toolchains, "base": lock.Bases} {
		for _, input := range inputs {
			if err := validateLockedInput(category, input, seen); err != nil {
				return err
			}
		}
	}
	for category, input := range map[string]*LockedInput{"kernel": lock.Kernel, "initramfs": lock.Initramfs} {
		if input != nil {
			if err := validateLockedInput(category, *input, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateLockedInput(category string, input LockedInput, seen map[string]bool) error {
	if input.Name == "" || len(input.Name) > 256 || strings.ContainsRune(input.Name, 0) || !validSHA256Digest(input.Digest) {
		return fmt.Errorf("project lock %s pin requires a bounded NUL-free name and sha256 digest", category)
	}
	key := category + "\x00" + input.Name
	if seen[key] {
		return fmt.Errorf("project lock contains duplicate %s pin %q", category, input.Name)
	}
	seen[key] = true
	return nil
}

func validSHA256Digest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

// CanonicalManifestDigest hashes the semantic YAML/JSON document, not its
// whitespace, comments or map ordering.
func CanonicalManifestDigest(raw []byte) (string, error) {
	var value any
	if err := yaml.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("decode project manifest for digest: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("canonicalize project manifest: %w", err)
	}
	sum := sha256.Sum256(append([]byte("platform-factory/project-plan/v2\x00"), canonical...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// VerifyLockPlan rejects semantic manifest drift for v2 locks. V1 remains
// loadable solely for migration compatibility and carries no plan digest.
func VerifyLockPlan(lock Lock, manifest []byte) error {
	if lock.Version == 1 {
		return nil
	}
	digest, err := CanonicalManifestDigest(manifest)
	if err != nil {
		return err
	}
	if digest != lock.PlanDigest {
		return fmt.Errorf("project plan and lock disagree: manifest is %s, lock pins %s; regenerate the lock", digest, lock.PlanDigest)
	}
	return nil
}

// VerifyAdjacentLock validates a project's canonical config/lock pair before
// a mutating build. Projects without a lock and legacy v1 locks remain
// loadable for compatibility; every generated v2 lock is enforced.
func (loaded Loaded) VerifyAdjacentLock() error {
	filename := loaded.AdjacentLockPath()
	lock, err := LoadLock(filename)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(loaded.File)
	if err != nil {
		return fmt.Errorf("read project manifest for lock verification: %w", err)
	}
	if err := VerifyLockPlan(lock, raw); err != nil {
		return err
	}
	if lock.Version == CurrentLockVersion {
		pins := append(append([]LockedInput{}, lock.Sources...), lock.Toolchains...)
		if lock.Kernel != nil {
			pins = append(pins, *lock.Kernel)
		}
		if lock.Initramfs != nil {
			pins = append(pins, *lock.Initramfs)
		}
		if err := loaded.verifyLocalPins(pins); err != nil {
			return err
		}
		if loaded.Config.Isolation == "microvm" && (lock.Kernel == nil || lock.Initramfs == nil) {
			return errors.New("microvm project lock must pin both kernel and initramfs; run `pf freeze --kernel PATH --initramfs PATH`")
		}
	}
	return nil
}

func (loaded Loaded) PinLocalFile(name string) (LockedInput, error) {
	localName := filepath.FromSlash(name)
	if filepath.IsAbs(localName) || filepath.ToSlash(filepath.Clean(localName)) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return LockedInput{}, fmt.Errorf("boot artifact %q is not a confined relative path", name)
	}
	filename := filepath.Join(loaded.Root, localName)
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return LockedInput{}, fmt.Errorf("boot artifact %q is missing or unsafe", name)
	}
	file, err := os.Open(filename)
	if err != nil {
		return LockedInput{}, err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return LockedInput{}, copyErr
	}
	if closeErr != nil {
		return LockedInput{}, closeErr
	}
	return LockedInput{Name: name, Digest: "sha256:" + hex.EncodeToString(hash.Sum(nil))}, nil
}

func (loaded Loaded) AdjacentLockPath() string {
	name := "pf.lock"
	base := filepath.Base(loaded.File)
	if base == "platform-factory.yaml" || base == "platform-factory.yml" {
		name = "platform-factory.lock"
	}
	return filepath.Join(filepath.Dir(loaded.File), name)
}

func (loaded Loaded) verifyLocalPins(pins []LockedInput) error {
	for _, pin := range pins {
		localName := filepath.FromSlash(pin.Name)
		if filepath.IsAbs(localName) || filepath.ToSlash(filepath.Clean(localName)) != pin.Name || pin.Name == "." || pin.Name == ".." || strings.HasPrefix(pin.Name, "../") {
			return fmt.Errorf("locked input %q is not a confined relative path", pin.Name)
		}
		filename := filepath.Join(loaded.Root, localName)
		info, err := os.Lstat(filename)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("locked input %q is missing or unsafe", pin.Name)
		}
		file, err := os.Open(filename)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		actual := "sha256:" + hex.EncodeToString(hash.Sum(nil))
		if actual != pin.Digest {
			return fmt.Errorf("locked input %q changed: got %s, want %s", pin.Name, actual, pin.Digest)
		}
	}
	return nil
}
