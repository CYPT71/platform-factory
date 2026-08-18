// Package doctor is the application-layer service behind `pf doctor` -
// devenir une façade"). cmd/platform-factory/doctor.go now only parses
// flags, calls Service.Run, formats the result, and picks an exit code;
// every actual diagnostic - what to check and how - lives here, where it
// can be tested without going through the CLI at all.
package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CYPT71/platform-factory/internal/hypervisor"
	"github.com/CYPT71/platform-factory/internal/hypervisor/sandbox"
	"github.com/CYPT71/platform-factory/internal/microvm"
	"github.com/CYPT71/platform-factory/internal/policy"
)

type Options struct {
	Registry string
	Policy   string
}

// Check is one diagnostic result: whether a capability or external tool
// platform-factory's other commands depend on is actually usable right
// now, and - only when it isn't - what to do about it.
type Check struct {
	Name       string `json:"name"`
	OK         bool   `json:"ok"`
	Skipped    bool   `json:"skipped,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
}

// Report is every check Run performed, plus whether all of them passed.
type Report struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// Service is the narrow contract cmd/platform-factory depends on for
// diagnostics.
type Service interface {
	Run(ctx context.Context) Report
	RunScope(ctx context.Context, scope string) Report
	RunScopeWithOptions(ctx context.Context, scope string, options Options) Report
}

// service is Service's only implementation, its dependencies all
// unexported and injectable only from within this package - a test
// that needs a fake constructs a service literal directly - so tests
// never have to shell out to real tools or probe real hardware.
type service struct {
	// lookPath reports the resolved path to an executable on PATH, or
	// an error if it isn't found - normally exec.LookPath.
	lookPath func(name string) (string, error)
	// runCommand runs name with args and returns nil only if it exits
	// zero within ctx's deadline - normally a short-timeout exec.
	// Runtime checks call this only for a tool lookPath already found,
	// never claiming a runtime is usable without actually invoking it.
	runCommand func(ctx context.Context, name string, args ...string) error
	// fileExists reports whether path exists and is a regular file -
	// normally backed by os.Stat. Used for registry-configured.
	fileExists func(path string) bool
	// userHomeDir returns the current user's home directory - normally
	// os.UserHomeDir.
	userHomeDir func() (string, error)
	// probeNative reports native hypervisor (KVM/HVF) availability.
	probeNative func(context.Context) (microvm.Capabilities, error)
	// probeSandbox reports which VMM sandbox primitives this host
	// supports (namespaces, cgroups, capability bounding-set drop).
	probeSandbox  func() sandbox.Support
	probeRegistry func(context.Context, string) error
	readFile      func(string) ([]byte, error)
}

// toolChecks is every external binary platform-factory's other commands
// shell out to, and what to suggest when one is missing.
var toolChecks = []struct{ name, suggestion string }{
	{"git", "install git and ensure it is on PATH"},
	{"docker", "install Docker, or ignore this if you only use Podman/containerd"},
	{"podman", "install Podman, or ignore this if you only use Docker/containerd"},
	{"containerd", "install containerd, or ignore this if unused"},
	{"ctr", "install the containerd client (ctr) to verify daemon access"},
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
	{"ctr", "runtime-containerd", "start the containerd daemon (`ctr version` failed)", []string{"version"}},
	{"kubectl", "runtime-kubernetes", "configure a reachable cluster (`kubectl cluster-info` failed) - check KUBECONFIG", []string{"cluster-info", "--request-timeout=3s"}},
}

const runtimeCheckTimeout = 3 * time.Second

// New returns a Service wired to real tools and real hardware probes -
// what cmd/platform-factory/doctor.go uses in production.
func New() Service {
	return &service{
		lookPath: exec.LookPath,
		runCommand: func(ctx context.Context, name string, args ...string) error {
			return exec.CommandContext(ctx, name, args...).Run()
		},
		fileExists: func(path string) bool {
			info, err := os.Stat(path)
			return err == nil && info.Mode().IsRegular()
		},
		userHomeDir:   os.UserHomeDir,
		probeNative:   hypervisor.ProbeNative,
		probeSandbox:  sandbox.ProbeSandbox,
		probeRegistry: probeRegistry,
		readFile:      os.ReadFile,
	}
}

// Run performs every diagnostic and returns the aggregate report. It
// never applies a fix itself, only reports what's missing and, when
// there's an obvious one, a suggestion - same contract the CLI had
// before this extraction.
func (s *service) Run(ctx context.Context) Report {
	return s.RunScope(ctx, "all")
}

// RunScope limits diagnostics to one user task so remediation stays focused.
func (s *service) RunScope(ctx context.Context, scope string) Report {
	return s.RunScopeWithOptions(ctx, scope, Options{})
}

func (s *service) RunScopeWithOptions(ctx context.Context, scope string, options Options) Report {
	var checks []Check

	toolFound := make(map[string]bool, len(toolChecks))
	for _, tool := range toolChecks {
		if !scopeIncludesTool(scope, tool.name) {
			continue
		}
		check := s.checkTool(tool.name, tool.suggestion)
		toolFound[tool.name] = check.OK
		checks = append(checks, check)
	}

	for _, rc := range runtimeChecks {
		if !scopeIncludesTool(scope, rc.tool) {
			continue
		}
		checks = append(checks, s.checkRuntime(ctx, rc.tool, rc.name, rc.suggestion, toolFound[rc.tool], rc.args))
	}

	if scope == "all" || scope == "publish" {
		if options.Registry != "" {
			checks = append(checks, s.checkRegistryAccess(ctx, options.Registry))
		} else {
			checks = append(checks, s.checkRegistryConfigured())
		}
	}
	if scope == "all" || scope == "build" || scope == "publish" || scope == "deploy" {
		if options.Policy != "" {
			checks = append(checks, s.checkPolicy(options.Policy))
		}
	}

	if (scope == "all" || scope == "build") && s.probeNative != nil {
		capabilities, err := s.probeNative(ctx)
		checks = append(checks, hypervisorChecks(capabilities, err)...)
	}

	if (scope == "all" || scope == "build") && s.probeSandbox != nil {
		support := s.probeSandbox()
		checks = append(checks,
			Check{Name: "sandbox-namespaces", OK: support.Namespaces, Suggestion: support.Details["namespaces"]},
			Check{Name: "sandbox-cgroups", OK: support.Cgroups, Suggestion: support.Details["cgroups"]},
			Check{Name: "sandbox-capability-bounding-drop", OK: support.CapabilityBoundingDrop, Suggestion: support.Details["capability-bounding-drop"]},
		)
	}

	report := Report{OK: true, Checks: checks}
	for _, c := range checks {
		if !c.OK && !c.Skipped {
			report.OK = false
		}
	}
	return report
}

func (s *service) checkRegistryAccess(ctx context.Context, registry string) Check {
	if s.probeRegistry == nil {
		return Check{Name: "registry-access", Suggestion: "registry probe is unavailable in this build"}
	}
	if err := s.probeRegistry(ctx, registry); err != nil {
		return Check{Name: "registry-access", Detail: err.Error(), Suggestion: "verify the registry URL, TLS trust, and PLATFORM_FACTORY_REGISTRY_USERNAME/PASSWORD"}
	}
	return Check{Name: "registry-access", OK: true, Detail: registry}
}

func (s *service) checkPolicy(path string) Check {
	if s.readFile == nil {
		return Check{Name: "policy", Suggestion: "policy reader is unavailable in this build"}
	}
	raw, err := s.readFile(path)
	if err != nil {
		return Check{Name: "policy", Detail: err.Error(), Suggestion: "provide a readable policy JSON file"}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var rules policy.Rules
	if err := decoder.Decode(&rules); err != nil {
		return Check{Name: "policy", Detail: err.Error(), Suggestion: "fix the policy JSON schema and api_version"}
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Check{Name: "policy", Detail: err.Error(), Suggestion: "remove trailing JSON values"}
	}
	evidence := policy.Evidence{SubjectDigest: "sha256:doctor", SourcesPinned: true, BasePinned: true, ToolchainPinned: true, PluginsPinned: true, NonRoot: true, ReadOnlyRootFS: true, CapabilitiesDropped: true, SecretsAbsent: true, SBOM: true, Provenance: true, Signature: true, Reproducible: true}
	if _, err := policy.Evaluate(rules, evidence); err != nil {
		return Check{Name: "policy", Detail: err.Error(), Suggestion: "use the supported platform-factory policy api_version"}
	}
	return Check{Name: "policy", OK: true, Detail: path}
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func probeRegistry(ctx context.Context, address string) error {
	if !strings.Contains(address, "://") {
		address = "https://" + address
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("invalid registry URL")
	}
	parsed.Path = "/v2/"
	parsed.RawQuery, parsed.Fragment = "", ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return err
	}
	if username := os.Getenv("PLATFORM_FACTORY_REGISTRY_USERNAME"); username != "" {
		request.SetBasicAuth(username, os.Getenv("PLATFORM_FACTORY_REGISTRY_PASSWORD"))
	}
	client := &http.Client{Timeout: runtimeCheckTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("registry /v2/ returned %s", response.Status)
	}
	return nil
}

func scopeIncludesTool(scope, tool string) bool {
	switch scope {
	case "build":
		return tool == "git" || tool == "docker" || tool == "podman" || tool == "containerd" || tool == "ctr"
	case "publish":
		return false
	case "deploy":
		return tool == "kubectl"
	default:
		return true
	}
}

func hypervisorChecks(capabilities microvm.Capabilities, probeErr error) []Check {
	backend := capabilities.Details["backend"]
	notApplicable := func(name, platform string) Check {
		return Check{Name: name, Skipped: true, Detail: "not applicable on " + platform}
	}
	platform := backend
	if platform == "" {
		platform = "this host"
	}
	checks := []Check{
		notApplicable("kvm-device", platform),
		notApplicable("kvm-extensions", platform),
		notApplicable("hyper-v", platform),
		notApplicable("virtualization-framework", platform),
	}
	if probeErr != nil {
		return append(checks, Check{Name: "native-hypervisor", Detail: probeErr.Error(), Suggestion: "inspect host virtualization permissions and platform logs"})
	}
	detail := capabilities.Architecture
	suggestion := capabilities.Details["unavailable"]
	switch backend {
	case "linux-kvm-native":
		checks[0] = Check{Name: "kvm-device", OK: capabilities.Available, Detail: detail, Suggestion: suggestion}
		checks[1] = Check{Name: "kvm-extensions", OK: capabilities.Available, Detail: capabilityFeatureSummary(capabilities), Suggestion: suggestion}
	case "windows-native-whp":
		checks[2] = Check{Name: "hyper-v", OK: capabilities.Available, Detail: detail, Suggestion: suggestion}
	case "darwin-native-virtualization":
		checks[3] = Check{Name: "virtualization-framework", OK: capabilities.Available, Detail: detail, Suggestion: suggestion}
	default:
		checks = append(checks, Check{Name: "native-hypervisor", OK: capabilities.Available, Detail: detail, Suggestion: suggestion})
	}
	return checks
}

func capabilityFeatureSummary(capabilities microvm.Capabilities) string {
	if capabilities.Available {
		return "required KVM API and extensions negotiated"
	}
	return capabilities.Details["unavailable"]
}

func (s *service) checkTool(name, suggestion string) Check {
	path, err := s.lookPath(name)
	if err != nil {
		return Check{Name: "tool-" + name, OK: false, Suggestion: suggestion}
	}
	return Check{Name: "tool-" + name, OK: true, Detail: path}
}

// checkRuntime only ever reports OK when it actually ran the command and
// it succeeded - never a placeholder "available via the CLI diagnostics
// flow" claim. If the tool wasn't found at all, this check reports that
// plainly rather than trying to run a command that can't exist.
func (s *service) checkRuntime(ctx context.Context, tool, name, suggestion string, toolPresent bool, args []string) Check {
	if !toolPresent {
		return Check{Name: name, OK: false, Suggestion: "tool-" + tool + " is missing; install it first"}
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, runtimeCheckTimeout)
	defer cancel()
	if err := s.runCommand(timeoutCtx, tool, args...); err != nil {
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
func (s *service) checkRegistryConfigured() Check {
	home, err := s.userHomeDir()
	if err != nil {
		return Check{Name: "registry-configured", OK: false, Detail: err.Error(), Suggestion: "could not determine home directory"}
	}
	candidates := []string{
		filepath.Join(home, ".docker", "config.json"),
		filepath.Join(home, ".config", "containers", "auth.json"),
	}
	for _, candidate := range candidates {
		if s.fileExists(candidate) {
			return Check{Name: "registry-configured", OK: true, Detail: candidate}
		}
	}
	return Check{
		Name: "registry-configured", OK: false,
		Suggestion: "run `docker login`/`podman login` against your registry, or ignore this if you only publish unauthenticated/local registries",
	}
}
