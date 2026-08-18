package build

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/CYPT71/platform-factory/internal/project"
)

// FailedBuild wraps a build callback's own reported failure (as opposed
// to a failure in ReproducibleBuild's own workspace bookkeeping) so a
// caller that needs its original signal back - `pf launch --publish`
// preserves build's exact CLI exit code - can recover it with
// errors.As instead of ReproducibleBuild inventing its own.
type FailedBuild struct{ Err error }

func (e *FailedBuild) Error() string { return e.Err.Error() }
func (e *FailedBuild) Unwrap() error { return e.Err }

// ReproducibleBuild runs build twice against loaded.Output(), in an
// isolated temporary workspace, so two independently-built layouts can
// be compared before anything is published: `pf launch --publish`'s
// reproducibility gate. It always leaves loaded.Output() in a good,
// buildable state - the pre-existing layout if there was one, otherwise
// the first candidate build - even when the two digests disagree or
// the second build fails outright; only build's own errors are wrapped
// in FailedBuild, every other error here is ReproducibleBuild's own
// workspace setup/teardown failing.
func ReproducibleBuild(loaded project.Loaded, build func() (digest string, err error)) (first, second string, err error) {
	output := loaded.Output()
	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", "", fmt.Errorf("prepare reproducibility workspace: %w", err)
	}
	workspace, err := os.MkdirTemp(parent, ".reproducibility-*")
	if err != nil {
		return "", "", fmt.Errorf("prepare reproducibility workspace: %w", err)
	}
	defer os.RemoveAll(workspace)
	previous := filepath.Join(workspace, "previous")
	if _, err := os.Stat(output); err == nil {
		if err := os.Rename(output, previous); err != nil {
			return "", "", fmt.Errorf("preserve previous layout: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("inspect previous layout: %w", err)
	}
	restore := func(candidate string) {
		if _, statErr := os.Stat(output); errors.Is(statErr, os.ErrNotExist) {
			_ = os.Rename(candidate, output)
		}
	}
	first, buildErr := build()
	if buildErr != nil {
		restore(previous)
		return "", "", &FailedBuild{Err: buildErr}
	}
	firstLayout := filepath.Join(workspace, "first")
	if err := os.Rename(output, firstLayout); err != nil {
		restore(previous)
		return "", "", fmt.Errorf("preserve first reproducibility build: %w", err)
	}
	second, buildErr = build()
	if buildErr != nil {
		restore(firstLayout)
		return first, "", &FailedBuild{Err: buildErr}
	}
	if first != second {
		// A divergent candidate must not silently replace the last usable
		// layout. Prefer the pre-existing layout, otherwise retain the first
		// independently built candidate for diagnosis.
		if err := os.RemoveAll(output); err != nil {
			return first, second, fmt.Errorf("remove divergent layout: %w", err)
		}
		if _, err := os.Stat(previous); err == nil {
			restore(previous)
		} else {
			restore(firstLayout)
		}
	}
	return first, second, nil
}
