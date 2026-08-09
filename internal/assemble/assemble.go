// Package assemble bridges a pipeline's declared, cached stage outputs
// to a real OCI image layout. It does not run stages itself and does not
// depend on internal/executor or internal/pipeline: callers supply an
// OutputResolver shaped exactly like (*executor.CachingRunner).Output.
package assemble

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/CYPT71/secure-oci-base/internal/core"
	"github.com/CYPT71/secure-oci-base/internal/oci"
)

// OutputResolver looks up the content descriptor a stage produced for a
// named artifact.
type OutputResolver func(stage, artifact string) (core.Descriptor, bool)

// Extract resolves every entry in definition.Outputs via resolve and
// copies each one's content out of store into dir, named by Output.Name.
// It returns the local path written for each output name. Extracted files
// are written 0644; callers needing an executable file must chmod it
// themselves.
func Extract(store core.CacheStore, definition core.Pipeline, resolve OutputResolver, dir string) (map[string]string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("assemble: %w", err)
	}
	paths := make(map[string]string, len(definition.Outputs))
	for _, output := range definition.Outputs {
		descriptor, ok := resolve(output.Stage, output.Artifact)
		if !ok {
			return nil, fmt.Errorf("assemble: output %q: stage %q has not produced artifact %q", output.Name, output.Stage, output.Artifact)
		}
		reader, err := store.Get(descriptor.Digest)
		if err != nil {
			return nil, fmt.Errorf("assemble: output %q: %w", output.Name, err)
		}
		path := filepath.Join(dir, output.Name)
		err = writeFile(path, reader)
		_ = reader.Close()
		if err != nil {
			return nil, fmt.Errorf("assemble: output %q: %w", output.Name, err)
		}
		paths[output.Name] = path
	}
	return paths, nil
}

// Image extracts definition's outputs into a scratch directory, wires the
// output named binaryOutput into opts.Binary (marking it executable), adds
// an oci.ExtraFile for every (output name -> container destination) pair
// in extraDests, and calls oci.Build.
func Image(store core.CacheStore, definition core.Pipeline, resolve OutputResolver, binaryOutput string, extraDests map[string]string, opts oci.Options) (string, error) {
	dir, err := os.MkdirTemp("", "platform-factory-assemble-*")
	if err != nil {
		return "", fmt.Errorf("assemble: %w", err)
	}
	defer os.RemoveAll(dir)

	paths, err := Extract(store, definition, resolve, dir)
	if err != nil {
		return "", err
	}

	binaryPath, ok := paths[binaryOutput]
	if !ok {
		return "", fmt.Errorf("assemble: binary output %q is not among the pipeline's declared outputs", binaryOutput)
	}
	if err := os.Chmod(binaryPath, 0755); err != nil {
		return "", fmt.Errorf("assemble: %w", err)
	}
	opts.Binary = binaryPath

	for name, dest := range extraDests {
		path, ok := paths[name]
		if !ok {
			return "", fmt.Errorf("assemble: extra file output %q is not among the pipeline's declared outputs", name)
		}
		opts.ExtraFiles = append(opts.ExtraFiles, oci.ExtraFile{Dest: dest, Source: path, Mode: 0555})
	}

	return oci.Build(opts)
}

func writeFile(path string, r io.Reader) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, err = io.Copy(file, r)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}
