package atomicfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WriteJSON marshals data as indented JSON and installs it atomically
// at dir/name, creating dir (mode 0755) if needed. The file itself is
// mode 0644: this repo's convention for informational reports (build
// metrics, SBOMs, provenance) that are meant to be read by anyone with
// access to the output directory.
func WriteJSON(dir, name string, data any) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return Write(dir, name, append(encoded, '\n'), 0o644, true)
}

// WriteJSONSensitive is WriteJSON with 0700/0600 permissions instead of
// 0755/0644, for documents that gate a security decision (publication
// policy, signed evidence, deployment identity) rather than being a
// plain informational report.
func WriteJSONSensitive(path string, data any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return Write(dir, filepath.Base(path), append(encoded, '\n'), 0o600, true)
}
