//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package provenance

import (
	"errors"
	"os"
)

func openMigrationProvenanceRoot(string) (*os.File, error) {
	return nil, errors.New("migration provenance: platform cannot guarantee fd-relative no-follow persistence")
}

func readMigrationProvenanceDir(*os.File) ([]os.DirEntry, error) {
	return nil, errors.New("migration provenance: unsupported platform")
}

func readMigrationProvenanceRecord(*os.File, string, int64) ([]byte, error) {
	return nil, errors.New("migration provenance: unsupported platform")
}

func appendMigrationProvenanceRecord(*os.File, string, []byte) error {
	return errors.New("migration provenance: unsupported platform")
}
