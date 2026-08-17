// Package docutil reads a Go package's doc comment straight off disk -
// the first line of the comment block immediately preceding "package
// NAME" in one of its non-test source files - without a full go/doc
// parse. It is shared by internal/mcp/project and internal/mcp/core,
// which both need to describe real packages grounded in their own
// source rather than hand-written prose that could drift.
package docutil

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// PackageDoc extracts the doc comment immediately preceding a
// "package NAME" clause, first line only, from the first non-test .go
// file found in dir. Returns "" if none is found - many small packages
// have no package-level doc comment, and that absence is itself
// accurate information, not an error.
func PackageDoc(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if doc := packageDocFromFile(filepath.Join(dir, name)); doc != "" {
			return doc
		}
	}
	return ""
}

func packageDocFromFile(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	var pending []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "package "):
			for _, candidate := range pending {
				if isBuildConstraintComment(candidate) {
					continue
				}
				return strings.TrimSpace(strings.TrimPrefix(candidate, "//"))
			}
			return ""
		case strings.HasPrefix(trimmed, "//"):
			pending = append(pending, trimmed)
		case trimmed == "":
			continue
		default:
			pending = nil
		}
	}
	return ""
}

// isBuildConstraintComment reports whether a comment line is a build
// tag (//go:build ...) or the legacy // +build ... form, rather than
// prose documenting the package - these sit in the same position
// immediately above "package NAME" but are not a doc comment.
func isBuildConstraintComment(comment string) bool {
	return strings.HasPrefix(comment, "//go:build") || strings.HasPrefix(comment, "// +build")
}
