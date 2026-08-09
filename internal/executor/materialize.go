package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	"github.com/CYPT71/secure-oci-base/internal/core"
)

// artifactSource resolves a produced artifact to its content
// descriptor. *CachingRunner satisfies it.
type artifactSource interface {
	Output(stage, artifact string) (core.Descriptor, bool)
}

// StagingRunner materializes each stage's declared input artifacts into
// the stage filesystem before the inner runner executes it, copying the
// content from the content-addressed store and re-verifying its digest
// at consumption time rather than trusting the producer's earlier Put.
// It replaces the previous convention of stages sharing a flat root and
// reading each other's outputs by path.
type StagingRunner struct {
	inner   StageRunner
	root    string
	store   core.CacheStore
	sources artifactSource
}

// StageRunner is the minimal run interface StagingRunner wraps.
type StageRunner interface {
	Run(ctx context.Context, stage core.Stage) error
}

type StageRunnerFunc func(context.Context, core.Stage) error

func (f StageRunnerFunc) Run(ctx context.Context, stage core.Stage) error { return f(ctx, stage) }

// NewStagingRunner returns a StagingRunner. root is the shared stage
// root that input targets are resolved under (via MapPath); sources
// resolves producing-stage outputs to content descriptors.
func NewStagingRunner(inner StageRunner, root string, store core.CacheStore, sources artifactSource) *StagingRunner {
	return &StagingRunner{inner: inner, root: root, store: store, sources: sources}
}

// Run materializes stage inputs, then delegates to the inner runner.
func (s *StagingRunner) Run(ctx context.Context, stage core.Stage) error {
	for _, input := range stage.Inputs {
		if err := s.materialize(stage.ID, input); err != nil {
			return err
		}
	}
	return s.inner.Run(ctx, stage)
}

func (s *StagingRunner) materialize(stageID string, input core.ArtifactReference) error {
	descriptor, ok := s.sources.Output(input.Stage, input.Name)
	if !ok {
		return fmt.Errorf("staging runner: stage %q: producer %q has not published artifact %q",
			stageID, input.Stage, input.Name)
	}
	target := input.Target
	if target == "" {
		target = path.Join("/inputs", input.Stage, input.Name)
	}
	destination := MapPath(s.root, target)
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("staging runner: stage %q: prepare %s: %w", stageID, target, err)
	}
	reader, err := s.store.Get(descriptor.Digest)
	if err != nil {
		return fmt.Errorf("staging runner: stage %q: read artifact %q: %w", stageID, input.Name, err)
	}
	defer reader.Close()

	temporary, err := os.CreateTemp(filepath.Dir(destination), ".staging-*")
	if err != nil {
		return fmt.Errorf("staging runner: stage %q: %w", stageID, err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hash), reader)
	closeErr := temporary.Close()
	if err != nil {
		return fmt.Errorf("staging runner: stage %q: copy artifact %q: %w", stageID, input.Name, err)
	}
	if closeErr != nil {
		return fmt.Errorf("staging runner: stage %q: %w", stageID, closeErr)
	}
	digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if digest != descriptor.Digest || written != descriptor.Size {
		return fmt.Errorf("staging runner: stage %q: artifact %q digest %s (%d bytes) does not match the producer %s (%d bytes)",
			stageID, input.Name, digest, written, descriptor.Digest, descriptor.Size)
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		return fmt.Errorf("staging runner: stage %q: install %s: %w", stageID, target, err)
	}
	return nil
}
