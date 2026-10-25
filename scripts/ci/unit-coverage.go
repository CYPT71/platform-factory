//go:build ignore

// Filter entrypoint and exec-only blocks from the unit coverage profile.
// The unfiltered profile remains a published CI artifact. These paths require
// the mandatory MicroVM/sandbox integration jobs; they are not unit-test skips.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type span struct{ first, last int }

// These files operate on a booted guest or privileged host TAP interfaces.
// Portable policy, guest supervision and virtio device logic remain in scope.
var integrationFiles = map[string]bool{
	"cmd/microvm-init/console_linux.go":                  true,
	"cmd/microvm-init/guest_device_linux.go":             true,
	"cmd/microvm-init/identity_linux.go":                 true,
	"cmd/microvm-init/net_report_linux.go":               true,
	"cmd/microvm-init/poweroff_linux.go":                 true,
	"cmd/platform-factory/microvm_native_linux_amd64.go": true,
	"internal/hypervisor/kvm/linux_tap_linux.go":         true,
}

// Helpers mutate process limits, mount namespaces and finally syscall.Exec.
// Exec replaces the Go runtime before its counters can be flushed. Keep the
// same file's configuration and permission-selection functions in scope.
var execFunctions = map[string]bool{
	"MaybeApplyPluginSandboxHelper": true, "applySandbox": true,
	"applySandboxWithConfig": true, "applyResourceLimits": true,
	"isolateTempDirectory": true, "openExecutable": true,
	"execPlugin": true, "pluginSandboxFatal": true,
}

func main() {
	profile := flag.String("profile", "coverage.filtered", "input text profile")
	root := flag.String("root", ".", "repository source root")
	flag.Parse()
	if err := filter(*profile, *root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func filter(profile, root string) error {
	input, err := os.Open(profile)
	if err != nil {
		return err
	}
	defer input.Close()
	scanner := bufio.NewScanner(input)
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "mode: ") {
		return fmt.Errorf("invalid coverage header")
	}
	output := bufio.NewWriter(os.Stdout)
	if _, err := fmt.Fprintln(output, scanner.Text()); err != nil {
		return err
	}
	cache := map[string][]span{}
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return fmt.Errorf("invalid coverage block")
		}
		position := strings.LastIndex(fields[0], ":")
		if position < 0 {
			return fmt.Errorf("missing coverage position")
		}
		source := strings.TrimPrefix(fields[0][:position], "github.com/CYPT71/platform-factory/")
		if source == fields[0][:position] || filepath.IsAbs(source) || strings.Contains(source, "..") {
			return fmt.Errorf("unexpected coverage source %q", source)
		}
		if integrationFiles[source] {
			continue
		}
		location := strings.Split(fields[0][position+1:], ",")
		if len(location) != 2 {
			return fmt.Errorf("invalid coverage location")
		}
		first, err := strconv.Atoi(strings.Split(location[0], ".")[0])
		if err != nil {
			return err
		}
		last, err := strconv.Atoi(strings.Split(location[1], ".")[0])
		if err != nil {
			return err
		}
		spans, found := cache[source]
		if !found {
			fs := token.NewFileSet()
			tree, err := parser.ParseFile(fs, filepath.Join(root, filepath.FromSlash(source)), nil, 0)
			if err != nil {
				return err
			}
			for _, decl := range tree.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				entry := tree.Name.Name == "main" && fn.Name.Name == "main" && fn.Recv == nil
				helper := source == "internal/plugin/sandbox_linux.go" && execFunctions[fn.Name.Name]
				if entry || helper {
					spans = append(spans, span{fs.Position(fn.Body.Pos()).Line, fs.Position(fn.Body.End()).Line})
				}
			}
			cache[source] = spans
		}
		omit := false
		for _, s := range spans {
			if first >= s.first && last <= s.last {
				omit = true
				break
			}
		}
		if !omit {
			if _, err := fmt.Fprintln(output, line); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return output.Flush()
}
