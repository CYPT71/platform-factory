package dockerarchive

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

const (
	maxFiles      = 10000
	maxTotalBytes = int64(2 << 30)
	maxEntryBytes = int64(1 << 30)
	maxJSONBytes  = int64(16 << 20)
)

type Report struct {
	Valid  bool     `json:"valid"`
	Images int      `json:"images"`
	Layers int      `json:"layers"`
	Tags   []string `json:"tags"`
}

type manifestEntry struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags"`
	Layers   []string `json:"Layers"`
}

// Verify validates a Docker Save archive without extracting it or invoking a
// container runtime. It accepts regular files only and verifies every
// content-addressed config/layer name against its bytes.
func Verify(ctx context.Context, reader io.Reader) (Report, error) {
	tr := tar.NewReader(reader)
	seen := map[string]bool{}
	digests := map[string]string{}
	sizes := map[string]int64{}
	var manifest []byte
	files, total := 0, int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Report{}, errors.New("docker archive: invalid or truncated tar")
		}
		name := strings.TrimSuffix(header.Name, "/")
		clean := path.Clean(name)
		if name == "" || strings.HasPrefix(name, "/") || clean != name || clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsRune(name, 0) {
			return Report{}, fmt.Errorf("docker archive: unsafe path %q", header.Name)
		}
		if seen[name] {
			return Report{}, fmt.Errorf("docker archive: duplicate path %q", name)
		}
		seen[name] = true
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return Report{}, fmt.Errorf("docker archive: non-regular entry %q", name)
		}
		files++
		if files > maxFiles || header.Size < 0 || header.Size > maxEntryBytes || header.Size > maxTotalBytes-total {
			return Report{}, errors.New("docker archive: resource limit exceeded")
		}
		total += header.Size
		hash := sha256.New()
		var target io.Writer = hash
		var buffer strings.Builder
		if name == "manifest.json" {
			if header.Size > maxJSONBytes {
				return Report{}, errors.New("docker archive: manifest.json exceeds limit")
			}
			target = io.MultiWriter(hash, &buffer)
		}
		written, err := io.Copy(target, io.LimitReader(tr, header.Size+1))
		if err != nil || written != header.Size {
			return Report{}, errors.New("docker archive: truncated entry")
		}
		digests[name], sizes[name] = hex.EncodeToString(hash.Sum(nil)), header.Size
		if name == "manifest.json" {
			manifest = []byte(buffer.String())
		}
	}
	if len(manifest) == 0 {
		return Report{}, errors.New("docker archive: manifest.json is missing")
	}
	decoder := json.NewDecoder(strings.NewReader(string(manifest)))
	decoder.DisallowUnknownFields()
	var entries []manifestEntry
	if err := decoder.Decode(&entries); err != nil || len(entries) == 0 {
		return Report{}, errors.New("docker archive: invalid manifest.json")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Report{}, errors.New("docker archive: manifest.json must contain exactly one value")
	}
	report := Report{Valid: true, Images: len(entries)}
	for _, entry := range entries {
		if err := validateReference(entry.Config, digests, sizes); err != nil {
			return Report{}, err
		}
		if len(entry.Layers) == 0 {
			return Report{}, errors.New("docker archive: image has no layers")
		}
		for _, layer := range entry.Layers {
			if err := validateReference(layer, digests, sizes); err != nil {
				return Report{}, err
			}
			report.Layers++
		}
		for _, tag := range entry.RepoTags {
			if tag == "" || strings.ContainsAny(tag, "\x00\r\n") {
				return Report{}, errors.New("docker archive: invalid repository tag")
			}
			report.Tags = append(report.Tags, tag)
		}
	}
	return report, nil
}

func validateReference(name string, digests map[string]string, sizes map[string]int64) error {
	clean := path.Clean(name)
	if name == "" || strings.HasPrefix(name, "/") || clean != name || clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsRune(name, 0) {
		return fmt.Errorf("docker archive: unsafe referenced path %q", name)
	}
	if _, ok := sizes[name]; !ok {
		return fmt.Errorf("docker archive: referenced file %q is missing", name)
	}
	base := path.Base(name)
	stem := strings.TrimSuffix(base, path.Ext(base))
	if len(stem) == 64 {
		if _, err := hex.DecodeString(stem); err != nil || stem != strings.ToLower(stem) {
			return fmt.Errorf("docker archive: invalid content-addressed name %q", name)
		}
		if digests[name] != stem {
			return fmt.Errorf("docker archive: digest mismatch for %q", name)
		}
	}
	return nil
}
