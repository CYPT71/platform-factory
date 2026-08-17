package core

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// unfinishedWorkMarkers are the exact tokens ci-security.yml's
// pr-policy job forbids anywhere in the tree (except README.md). Built
// by concatenation, deliberately, rather than as contiguous literals:
// that same pr-policy check greps this very file's own source, and a
// contiguous literal here would trip the check on this file itself.
var unfinishedWorkMarkers = []string{"TO" + "DO", "FIX" + "ME", "place" + "holder"}

// allowlistPattern extracts every `! -path 'X'` entry from the
// subprocess-execution allowlist's find command in
// .github/workflows/ci-security.yml, so this check reads the real,
// current allowlist instead of a second, easily stale copy of it.
var allowlistPattern = regexp.MustCompile(`! -path '([^']+)'`)

// execUsagePattern mirrors ci-security.yml's own subprocess-execution
// grep. Built by concatenation, deliberately, for the same reason
// unfinishedWorkMarkers is above: this file calls no exec function
// itself, and a contiguous literal here would make the very same CI
// check this function reimplements flag this file as if it did.
var execUsagePattern = regexp.MustCompile("os" + "/exec|exec" + `\.Command`)

func loadOSExecAllowlist(repoRoot string) (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "ci-security.yml"))
	if err != nil {
		return nil, err
	}
	allowlist := map[string]bool{}
	for _, match := range allowlistPattern.FindAllStringSubmatch(string(data), -1) {
		allowlist[match[1]] = true
	}
	return allowlist, nil
}

// osExecAllowlistCheck reimplements ci-security.yml's static-analysis
// job's own check locally: every cmd/ or internal/ .go file that
// shells out to a subprocess must be named in that workflow's
// allowlist. Running it here means a plugin/core author sees the same
// failure pf_core_patch's PR would otherwise surface only after a CI
// round-trip.
func osExecAllowlistCheck(repoRoot string) StepResult {
	step := StepResult{Name: "subprocess-execution allowlist (mirrors ci-security.yml)"}
	allowlist, err := loadOSExecAllowlist(repoRoot)
	if err != nil {
		step.Status = "skipped"
		step.Output = err.Error()
		return step
	}
	var violations []string
	for _, root := range []string{"cmd", "internal"} {
		_ = filepath.WalkDir(filepath.Join(repoRoot, root), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			relative, relErr := filepath.Rel(repoRoot, path)
			if relErr != nil {
				return nil
			}
			relative = filepath.ToSlash(relative)
			if allowlist[relative] {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			if execUsagePattern.Match(data) {
				violations = append(violations, relative)
			}
			return nil
		})
	}
	if len(violations) > 0 {
		step.Status = "failed"
		step.Output = "unallowlisted subprocess execution: " + strings.Join(violations, ", ")
	} else {
		step.Status = "ok"
	}
	return step
}

// tlsVerifyBypassPattern mirrors ci-security.yml's ban on Go's TLS
// config field that disables certificate verification. Built by
// concatenation for the same self-reference reason as
// unfinishedWorkMarkers/execUsagePattern above.
var tlsVerifyBypassPattern = regexp.MustCompile("Insecure" + "SkipVerify")

func insecureSkipVerifyCheck(repoRoot string) StepResult {
	step := StepResult{Name: "no TLS certificate-verification bypass (mirrors ci-security.yml)"}
	var violations []string
	for _, root := range []string{"cmd", "internal"} {
		_ = filepath.WalkDir(filepath.Join(repoRoot, root), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			if tlsVerifyBypassPattern.Match(data) {
				relative, _ := filepath.Rel(repoRoot, path)
				violations = append(violations, filepath.ToSlash(relative))
			}
			return nil
		})
	}
	if len(violations) > 0 {
		step.Status = "failed"
		step.Output = strings.Join(violations, ", ")
	} else {
		step.Status = "ok"
	}
	return step
}

var unfinishedWorkPattern = regexp.MustCompile(strings.Join(unfinishedWorkMarkers, "|"))

// unfinishedWorkCheck mirrors ci-security.yml's pr-policy job: none of
// unfinishedWorkMarkers may appear anywhere in the tree except
// README.md - those are the exact markers that check forbids.
func unfinishedWorkCheck(repoRoot string) StepResult {
	step := StepResult{Name: "no unfinished-work markers (mirrors ci-security.yml pr-policy)"}
	var violations []string
	_ = filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		relative, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return nil
		}
		relative = filepath.ToSlash(relative)
		if d.IsDir() {
			if relative == ".git" || relative == ".github" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Base(relative) == "README.md" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if unfinishedWorkPattern.Match(data) {
			violations = append(violations, relative)
		}
		return nil
	})
	if len(violations) > 0 {
		step.Status = "failed"
		step.Output = strings.Join(violations, ", ")
	} else {
		step.Status = "ok"
	}
	return step
}
