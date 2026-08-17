package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// goListPackage is the subset of `go list -json` output this package
// needs: enough to map a changed file to its package and to walk the
// reverse-dependency graph.
type goListPackage struct {
	Dir        string   `json:"Dir"`
	ImportPath string   `json:"ImportPath"`
	Deps       []string `json:"Deps"`
}

// changedGoFiles returns every .go file the working tree has added,
// modified, or left untracked, relative to repoRoot - the real,
// current diff, not a cached snapshot - by combining `git diff
// --name-only HEAD` (tracked changes, staged or not) with
// `git ls-files --others --exclude-standard` (untracked files).
func changedGoFiles(ctx context.Context, repoRoot string) ([]string, error) {
	tracked, err := gitOutput(ctx, repoRoot, "diff", "--name-only", "HEAD")
	if err != nil {
		return nil, err
	}
	untracked, err := gitOutput(ctx, repoRoot, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var files []string
	for _, line := range append(strings.Split(tracked, "\n"), strings.Split(untracked, "\n")...) {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasSuffix(line, ".go") || seen[line] {
			continue
		}
		seen[line] = true
		files = append(files, line)
	}
	return files, nil
}

func gitOutput(ctx context.Context, repoRoot string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func listPackages(ctx context.Context, repoRoot string) ([]goListPackage, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-json", "./...")
	cmd.Dir = repoRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list -json ./...: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	decoder := json.NewDecoder(&stdout)
	var packages []goListPackage
	for decoder.More() {
		var pkg goListPackage
		if err := decoder.Decode(&pkg); err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		packages = append(packages, pkg)
	}
	return packages, nil
}

// AffectedPackages computes the real reverse-dependency closure of every
// currently changed .go file: the changed files' own packages, plus
// every package that (transitively) imports one of them. Returns import
// paths, sorted, suitable to pass straight to `go test`.
func AffectedPackages(ctx context.Context, repoRoot string) ([]string, error) {
	changedFiles, err := changedGoFiles(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	if len(changedFiles) == 0 {
		return nil, nil
	}

	packages, err := listPackages(ctx, repoRoot)
	if err != nil {
		return nil, err
	}

	dirToImportPath := make(map[string]string, len(packages))
	for _, pkg := range packages {
		dirToImportPath[filepath.Clean(pkg.Dir)] = pkg.ImportPath
	}

	changedPackages := map[string]bool{}
	for _, file := range changedFiles {
		dir := filepath.Clean(filepath.Join(repoRoot, filepath.Dir(filepath.FromSlash(file))))
		if importPath, ok := dirToImportPath[dir]; ok {
			changedPackages[importPath] = true
		}
	}
	if len(changedPackages) == 0 {
		return nil, nil
	}

	affected := map[string]bool{}
	for importPath := range changedPackages {
		affected[importPath] = true
	}
	for {
		grew := false
		for _, pkg := range packages {
			if affected[pkg.ImportPath] {
				continue
			}
			for _, dep := range pkg.Deps {
				if affected[dep] {
					affected[pkg.ImportPath] = true
					grew = true
					break
				}
			}
		}
		if !grew {
			break
		}
	}

	result := make([]string, 0, len(affected))
	for importPath := range affected {
		result = append(result, importPath)
	}
	sort.Strings(result)
	return result, nil
}
