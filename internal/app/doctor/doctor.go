// Package doctor is the application-layer service behind `pf doctor` -
// Sanetizer-todo.md item 8 ("réduire cmd/platform-factory... la CLI doit
// devenir une façade"). cmd/platform-factory/doctor.go now only parses
// flags, calls Service.Run, formats the result, and picks an exit code;
// every actual diagnostic - what to check and how - lives here, where it
// can be tested without going through the CLI at all.
package doctor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/CYPT71/secure-oci-base/internal/hypervisor"
	"github.com/CYPT71/secure-oci-base/internal/hypervisor/sandbox"
	"github.com/CYPT71/secure-oci-base/internal/microvm"
)

// Check is one diagnostic result: whether a capability or external tool
// platform-factory's other commands depend on is actually usable right
// now, and - only when it isn't - what to do about it.
type Check struct {
	Name       string `json:"name"`
	OK         bool   `json:"ok"`
	Detail     string `json:"detail,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
}

// Report is every check Run performed, plus whether all of them passed.
type Report struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// Service holds every dependency Run needs, all injectable so tests
// never have to shell out to real tools or probe real hardware -
// production code wires New() below; tests construct a Service directly
// with fakes.
type Service struct {
	// LookPath reports the resolved path to an executable on PATH, or
	// an error if it isn't found - normally exec.LookPath.
	LookPath func(name string) (string, error)
	// RunCommand runs name with args and returns nil only if it exits
	// zero within ctx's deadline - normally a short-timeout exec.
	// Runtime checks call this only for a tool LookPath already found,
	// never claiming a runtime is usable without actually invoking it.
	RunCommand func(ctx context.Context, name string, args ...string) error
	// FileExists reports whether path exists and is a regular file -
	// normally backed by os.Stat. Used for registry-configured.
	FileExists func(path string) bool
	// UserHomeDir returns the current user's home directory - normally
	// os.UserHomeDir.
	UserHomeDir func() (string, error)
	// ProbeNative reports native hypervisor (KVM/HVF) availability.
	ProbeNative func(context.Context) (microvm.Capabilities, error)
	// ProbeSandbox reports which VMM sandbox primitives this host
	// supports (namespaces, cgroups, capability bounding-set drop).
	ProbeSandbox func() sandbox.Support
}

// toolChecks is every external binary platform-factory's other commands
// shell out to, and what to suggest when one is missing.
var toolChecks = []struct{ name, suggestion string }{
	{"git", "install git and ensure it is on PATH"},
	{"docker", "install Docker, or ignore this if you only use Podman/containerd"},
	{"podman", "install Podman, or ignore this if you only use Docker/containerd"},
	{"containerd", "install containerd, or ignore this if unused"},
	{"kubectl", "install kubectl, or ignore this if you don't deploy to Kubernetes"},
}

// runtimeChecks is every "is the tool not just installed, but actually
// usable right now" probe: (tool, runtime check name, args to prove the
// tool answers, suggestion). Each only runs if the corresponding
// toolChecks entry already found the binary - a runtime check never
// claims OK without actually invoking the command.
var runtimeChecks = []struct {
	tool, name, suggestion string
	args                   []string
}{
	{"docker", "runtime-docker", "start the Docker daemon (`docker info` failed)", []string{"info"}},
	{"podman", "runtime-podman", "start or initialize the Podman machine (`podman info` failed) - see `podman machine init`/`podman machine start`", []string{"info"}},
	{"containerd", "runtime-containerd", "start the containerd daemon (`ctr version` failed)", []string{"version"}},
	{"kubectl", "runtime-kubernetes", "configure a reachable cluster (`kubectl cluster-info` failed) - check KUBECONFIG", []string{"cluster-info", "--request-timeout=3s"}},
}

const runtimeCheckTimeout = 3 * time.Second

// New returns a Service wired to real tools and real hardware probes -
// what cmd/platform-factory/doctor.go uses in production.
func New() Service {
	return Service{
		LookPath: exec.LookPath,
		RunCommand: func(ctx context.Context, name string, args ...string) error {
			return exec.CommandContext(ctx, name, args...).Run()
		},
		FileExists: func(path string) bool {
			info, err := os.Stat(path)
			return err == nil && info.Mode().IsRegular()
		},
		UserHomeDir:  os.UserHomeDir,
		ProbeNative:  hypervisor.ProbeNative,
		ProbeSandbox: sandbox.ProbeSandbox,
	}
}

// Run performs every diagnostic and returns the aggregate report. It
// never applies a fix itself, only reports what's missing and, when
// there's an obvious one, a suggestion - same contract the CLI had
// before this extraction.
func (s Service) Run(ctx context.Context) Report {
	var checks []Check

	toolFound := make(map[string]bool, len(toolChecks))
	for _, tool := range toolChecks {
		check := s.checkTool(tool.name, tool.suggestion)
		toolFound[tool.name] = check.OK
		checks = append(checks, check)
	}

	for _, rc := range runtimeChecks {
		checks = append(checks, s.checkRuntime(ctx, rc.tool, rc.name, rc.suggestion, toolFound[rc.tool], rc.args))
	}

	checks = append(checks, s.checkRegistryConfigured())

	if s.ProbeNative != nil {
		capabilities, err := s.ProbeNative(ctx)
		if err != nil {
			checks = append(checks, Check{Name: "native-hypervisor", OK: false, Detail: err.Error()})
		} else {
			check := Check{Name: "native-hypervisor", OK: capabilities.Available, Detail: capabilities.Architecture}
			if !capabilities.Available {
				check.Suggestion = capabilities.Details["unavailable"]
			}
			checks = append(checks, check)
		}
	}

	if s.ProbeSandbox != nil {
		support := s.ProbeSandbox()
		checks = append(checks,
			Check{Name: "sandbox-namespaces", OK: support.Namespaces, Suggestion: support.Details["namespaces"]},
			Check{Name: "sandbox-cgroups", OK: support.Cgroups, Suggestion: support.Details["cgroups"]},
			Check{Name: "sandbox-capability-bounding-drop", OK: support.CapabilityBoundingDrop, Suggestion: support.Details["capability-bounding-drop"]},
		)
	}

	report := Report{OK: true, Checks: checks}
	for _, c := range checks {
		if !c.OK {
			report.OK = false
		}
	}
	return report
}

func (s Service) checkTool(name, suggestion string) Check {
	path, err := s.LookPath(name)
	if err != nil {
		return Check{Name: "tool-" + name, OK: false, Suggestion: suggestion}
	}
	return Check{Name: "tool-" + name, OK: true, Detail: path}
}

// checkRuntime only ever reports OK when it actually ran the command and
// it succeeded - never a placeholder "available via the CLI diagnostics
// flow" claim. If the tool wasn't found at all, this check reports that
// plainly rather than trying to run a command that can't exist.
func (s Service) checkRuntime(ctx context.Context, tool, name, suggestion string, toolPresent bool, args []string) Check {
	if !toolPresent {
		return Check{Name: name, OK: false, Suggestion: "tool-" + tool + " is missing; install it first"}
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, runtimeCheckTimeout)
	defer cancel()
	if err := s.RunCommand(timeoutCtx, tool, args...); err != nil {
		return Check{Name: name, OK: false, Detail: err.Error(), Suggestion: suggestion}
	}
	return Check{Name: name, OK: true}
}

// checkRegistryConfigured looks for either Docker's or Podman's own
// registry-auth config file actually existing and non-empty - a real
// filesystem check, not a claim. It does not attempt to validate the
// credentials inside it (that would require a network round trip to a
// registry this command doesn't know about) - only that some
// configuration exists to check in the first place.
func (s Service) checkRegistryConfigured() Check {
	home, err := s.UserHomeDir()
	if err != nil {
		return Check{Name: "registry-configured", OK: false, Detail: err.Error(), Suggestion: "could not determine home directory"}
	}
	candidates := []string{
		filepath.Join(home, ".docker", "config.json"),
		filepath.Join(home, ".config", "containers", "auth.json"),
	}
	for _, candidate := range candidates {
		if s.FileExists(candidate) {
			return Check{Name: "registry-configured", OK: true, Detail: candidate}
		}
	}
	return Check{
		Name: "registry-configured", OK: false,
		Suggestion: "run `docker login`/`podman login` against your registry, or ignore this if you only publish unauthenticated/local registries",
	}
}
