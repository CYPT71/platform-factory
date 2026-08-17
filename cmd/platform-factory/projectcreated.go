package main

import (
	"io/fs"
	"path/filepath"
	"time"
)

// projectCreatedSkipDirs are directories that hold generated output or
// fetched dependencies rather than the project's own source - none of
// them should influence "the project's first file" below.
var projectCreatedSkipDirs = map[string]bool{
	".git":              true,
	".platform-factory": true,
	".pf":               true,
	"node_modules":      true,
	"dist":              true,
	"reports":           true,
}

// earliestProjectFileTime walks root and returns the earliest filesystem
// birth time (creation time) among its regular files, skipping
// projectCreatedSkipDirs. It exists so a project build's OCI "created"
// annotation can be both reproducible (same inputs, same files, same
// answer every time) and meaningful (an actual date, not the Unix epoch)
// without depending on wall-clock time at build time. Platforms without a
// real birth time (see fileBirthTime's non-darwin stub) fall back to each
// file's modification time, which is still fully reproducible as long as
// the files themselves are not rewritten between builds.
//
// Returns the zero Time if root has no regular files outside the skipped
// directories - the caller is expected to fall back to something else in
// that case.
func earliestProjectFileTime(root string) time.Time {
	var earliest time.Time
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if path != root && projectCreatedSkipDirs[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return nil
		}
		born := fileBirthTime(info)
		if earliest.IsZero() || born.Before(earliest) {
			earliest = born
		}
		return nil
	})
	return earliest
}
