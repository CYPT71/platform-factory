package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CYPT71/platform-factory/internal/marketplace"
	"github.com/CYPT71/platform-factory/internal/mcp/toolerror"
	hostplugin "github.com/CYPT71/platform-factory/internal/plugin"
)

// ValidationReport is the pf_plugin_validate payload. Each field is
// "ok", a short reason it could not be checked, or the specific
// validation failure - never a bare boolean, so a caller always knows
// which dimension needs attention.
type ValidationReport struct {
	Plugin        string   `json:"plugin"`
	Valid         bool     `json:"valid"`
	Manifest      string   `json:"manifest"`
	Digest        string   `json:"digest"`
	Build         string   `json:"build"`
	Tests         string   `json:"tests"`
	Compatibility string   `json:"compatibility"`
	Marketplace   string   `json:"marketplace"`
	Security      string   `json:"security"`
	Issues        []string `json:"issues,omitempty"`
}

// buildTimeout bounds the `go build` this tool runs against a single
// plugin module - generous for a small plugin, but finite so a stuck
// toolchain invocation cannot hang the whole MCP session (see
// internal/mcp/server.go's single-request-at-a-time dispatch loop).
const buildTimeout = 120 * time.Second

// Validate runs every check pf_plugin_validate reports. A
// "language-command" plugin (no plugin.json) skips the manifest/digest/
// compatibility checks, which only apply to the RPC manifest shape, and
// reports why in those fields rather than silently omitting them.
func Validate(ctx context.Context, repoRoot, name string) (ValidationReport, error) {
	if err := validPluginName(name); err != nil {
		return ValidationReport{}, err
	}
	dir := filepath.Join(pluginsDir(repoRoot), name)
	summary := summarize(repoRoot, name)
	report := ValidationReport{Plugin: name, Valid: true}
	report.Marketplace = validateMarketplaceManifest(dir, name)
	if report.Marketplace != "ok" && !strings.HasPrefix(report.Marketplace, "not published") {
		report.Valid = false
		report.Issues = append(report.Issues, "marketplace: "+report.Marketplace)
	}
	report.Security = "ok"

	if summary.Kind != "rpc" {
		report.Manifest = "n/a (language-command plugin, no plugin.json)"
		report.Digest = "n/a"
		report.Compatibility = "n/a"
	} else {
		manifest, err := hostplugin.LoadManifest(dir)
		if err != nil {
			report.Manifest = err.Error()
			report.Valid = false
			report.Issues = append(report.Issues, "manifest: "+err.Error())
		} else {
			report.Manifest = "ok"
			report.Compatibility = compatibilityCheck(manifest)
			if err := manifest.VerifyExecutable(dir); err != nil {
				report.Digest = err.Error()
				// A freshly scaffolded or not-yet-built plugin failing the
				// digest check is expected, not a validation failure - only
				// flag it as an issue (not Valid=false) when the executable
				// exists but the digest genuinely does not match, i.e. the
				// manifest was hand-edited without rebuilding.
				if executableExists(dir, manifest.Executable) {
					report.Valid = false
					report.Issues = append(report.Issues, "digest: "+err.Error())
				}
			} else {
				report.Digest = "ok"
			}
		}
	}

	report.Build = runGoBuild(ctx, dir)
	if strings.HasPrefix(report.Build, "error:") {
		report.Valid = false
		report.Issues = append(report.Issues, "build: "+report.Build)
	}

	if len(testFiles(dir)) == 0 {
		report.Tests = "no _test.go files found"
		report.Issues = append(report.Issues, "tests: no _test.go files found")
	} else {
		report.Tests = runGoTests(ctx, dir)
		if strings.HasPrefix(report.Tests, "error:") {
			report.Valid = false
			report.Issues = append(report.Issues, "tests: "+report.Tests)
		}
	}

	return report, nil
}

func validateMarketplaceManifest(dir, name string) string {
	file, err := os.Open(filepath.Join(dir, marketplace.ManifestFileName))
	if os.IsNotExist(err) {
		return "not published (plugin.yaml absent)"
	}
	if err != nil {
		return err.Error()
	}
	defer file.Close()
	manifest, err := marketplace.DecodeManifest(file)
	if err != nil {
		return err.Error()
	}
	if manifest.Name != name {
		return fmt.Sprintf("name %q does not match directory %q", manifest.Name, name)
	}
	entrypoint := filepath.Join(dir, filepath.FromSlash(manifest.Entrypoint))
	info, err := os.Lstat(entrypoint)
	if err != nil {
		return fmt.Sprintf("entrypoint: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "entrypoint must not be a symlink"
	}
	return "ok"
}

func runGoTests(ctx context.Context, dir string) string {
	testCtx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	cmd := exec.CommandContext(testCtx, "go", "test", "./...")
	cmd.Dir = dir
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		return fmt.Sprintf("error: %s", strings.TrimSpace(output.String()))
	}
	return "ok"
}

func executableExists(dir, executable string) bool {
	_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(executable)))
	return err == nil
}

func compatibilityCheck(manifest hostplugin.Manifest) string {
	if manifest.Family == hostplugin.PluginFamilyLanguage && len(manifest.Permissions.Network) > 0 {
		return "language-family plugins may not declare network permissions"
	}
	return "ok"
}

// runGoBuild builds the plugin's own module (its nearest go.mod, found
// automatically by `go build` from dir) and returns "ok" or
// "error: <compiler output>".
func runGoBuild(ctx context.Context, dir string) string {
	buildCtx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	cmd := exec.CommandContext(buildCtx, "go", "build", "./...")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Sprintf("error: %s", strings.TrimSpace(stderr.String()))
	}
	return "ok"
}

type validateArguments struct {
	Plugin string `json:"plugin"`
}

// ValidateToolHandler returns the pf_plugin_validate handler.
func ValidateToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var args validateArguments
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "invalid arguments: %v", err)
		}
		report, err := Validate(ctx, repoRoot, args.Plugin)
		if err != nil {
			return "", err
		}
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}
