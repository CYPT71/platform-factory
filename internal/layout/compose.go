package layout

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const composeCopyBufferSize = 1 << 20

// Compose creates one deterministic OCI Image Layout containing every
// manifest from the supplied verified layouts. Blobs are content-addressed
// and therefore copied only once. Output must not already exist.
func Compose(output string, inputs []string) (Report, error) {
	if output == "" || len(inputs) < 2 {
		return Report{}, errors.New("output and at least two input layouts are required")
	}
	if _, err := os.Lstat(output); err == nil {
		return Report{}, fmt.Errorf("output already exists: %s", output)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Report{}, fmt.Errorf("stat output: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return Report{}, fmt.Errorf("create output parent: %w", err)
	}

	manifests := make([]descriptor, 0, len(inputs))
	for _, input := range inputs {
		if _, err := Verify(input); err != nil {
			return Report{}, fmt.Errorf("verify input %s: %w", input, err)
		}
		var idx index
		if err := decodeFile(filepath.Join(input, "index.json"), &idx); err != nil {
			return Report{}, fmt.Errorf("read input index %s: %w", input, err)
		}
		manifests = append(manifests, idx.Manifests...)
	}
	sort.Slice(manifests, func(i, j int) bool {
		return descriptorOrder(manifests[i]) < descriptorOrder(manifests[j])
	})

	temporary, err := os.MkdirTemp(filepath.Dir(output), ".oci-compose-")
	if err != nil {
		return Report{}, fmt.Errorf("create temporary layout: %w", err)
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(temporary)
		}
	}()
	blobDir := filepath.Join(temporary, "blobs", "sha256")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		return Report{}, err
	}
	if err := os.WriteFile(filepath.Join(temporary, "oci-layout"), []byte("{\"imageLayoutVersion\":\"1.0.0\"}\n"), 0o644); err != nil {
		return Report{}, err
	}

	buffer := make([]byte, composeCopyBufferSize)
	for _, input := range inputs {
		entries, err := os.ReadDir(filepath.Join(input, "blobs", "sha256"))
		if err != nil {
			return Report{}, err
		}
		for _, entry := range entries {
			destination := filepath.Join(blobDir, entry.Name())
			if _, err := os.Lstat(destination); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return Report{}, err
			}
			if err := copyBlob(
				filepath.Join(input, "blobs", "sha256", entry.Name()),
				destination,
				buffer,
			); err != nil {
				return Report{}, err
			}
		}
	}

	indexData, err := json.Marshal(index{SchemaVersion: 2, Manifests: manifests})
	if err != nil {
		return Report{}, err
	}
	indexData = append(indexData, '\n')
	if err := os.WriteFile(filepath.Join(temporary, "index.json"), indexData, 0o644); err != nil {
		return Report{}, err
	}
	report, err := Verify(temporary)
	if err != nil {
		return Report{}, fmt.Errorf("verify composed layout: %w", err)
	}
	if err := os.Rename(temporary, output); err != nil {
		return Report{}, fmt.Errorf("install composed layout: %w", err)
	}
	success = true
	report.Path = output
	return report, nil
}

func descriptorOrder(value descriptor) string {
	reference := value.Annotations["org.opencontainers.image.ref.name"]
	platformName := ""
	if value.Platform != nil {
		platformName = value.Platform.OS + "/" + value.Platform.Architecture
	}
	return strings.Join([]string{reference, platformName, value.Digest}, "\x00")
}

func copyBlob(sourceName, destinationName string, buffer []byte) error {
	source, err := os.Open(sourceName)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(destinationName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	copied := false
	defer func() {
		_ = destination.Close()
		if !copied {
			_ = os.Remove(destinationName)
		}
	}()
	if _, err := io.CopyBuffer(destination, source, buffer); err != nil {
		return err
	}
	if err := destination.Close(); err != nil {
		return err
	}
	if err := os.Chmod(destinationName, 0o644); err != nil {
		return err
	}
	copied = true
	return nil
}
