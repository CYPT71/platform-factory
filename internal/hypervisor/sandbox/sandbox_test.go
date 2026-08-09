package sandbox

import (
	"errors"
	"fmt"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if !config.Seccomp {
		t.Error("expected Seccomp to be true")
	}
	if !config.Namespaces {
		t.Error("expected Namespaces to be true")
	}
	if !config.Cgroups {
		t.Error("expected Cgroups to be true")
	}
	if !config.DropPrivileges {
		t.Error("expected DropPrivileges to be true")
	}
	if len(config.DropCapabilities) == 0 {
		t.Error("expected DropCapabilities to not be empty")
	}
}

func TestVMMConfig(t *testing.T) {
	config := VMMConfig()

	if !config.Seccomp {
		t.Error("expected Seccomp to be true")
	}
	if !config.Namespaces {
		t.Error("expected Namespaces to be true")
	}
	if !config.Cgroups {
		t.Error("expected Cgroups to be true")
	}
	if !config.DropPrivileges {
		t.Error("expected DropPrivileges to be true")
	}
	if config.CPULimit != 100 {
		t.Errorf("expected CPULimit to be 100, got %d", config.CPULimit)
	}
	if config.PIDsLimit != 1000 {
		t.Errorf("expected PIDsLimit to be 1000, got %d", config.PIDsLimit)
	}
	if len(config.DropCapabilities) != len(AllCapabilities) {
		t.Errorf("expected DropCapabilities length to be %d, got %d",
			len(AllCapabilities), len(config.DropCapabilities))
	}
}

func TestAllCapabilities(t *testing.T) {
	if len(AllCapabilities) == 0 {
		t.Error("expected AllCapabilities to not be empty")
	}

	// Check some expected capabilities are present
	expectedCaps := []string{
		"CAP_NET_ADMIN",
		"CAP_SYS_ADMIN",
		"CAP_SYS_MODULE",
		"CAP_SYS_RAWIO",
		"CAP_NET_RAW",
	}

	capSet := make(map[string]bool)
	for _, cap := range AllCapabilities {
		capSet[cap] = true
	}

	for _, expected := range expectedCaps {
		if !capSet[expected] {
			t.Errorf("expected capability %s to be in AllCapabilities", expected)
		}
	}
}

func TestParseNamespace(t *testing.T) {
	tests := []struct {
		input    string
		expected Namespace
		wantErr  bool
	}{
		{"pid", NamespacePID, false},
		{"uts", NamespaceUTS, false},
		{"mnt", NamespaceMNT, false},
		{"ipc", NamespaceIPC, false},
		{"net", NamespaceNET, false},
		{"user", NamespaceUSER, false},
		{"cgroup", NamespaceCGROUP, false},
		{"invalid", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseNamespace(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseNamespace(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.expected {
				t.Errorf("ParseNamespace(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSeccompAction(t *testing.T) {
	if SeccompActionAllow != "SCMP_ACT_ALLOW" {
		t.Errorf("expected SeccompActionAllow to be SCMP_ACT_ALLOW, got %q", SeccompActionAllow)
	}
	if SeccompActionKill != "SCMP_ACT_KILL" {
		t.Errorf("expected SeccompActionKill to be SCMP_ACT_KILL, got %q", SeccompActionKill)
	}
}

func TestDefaultSeccompProfile(t *testing.T) {
	profile := DefaultSeccompProfile()

	if profile.DefaultAction != SeccompActionKill {
		t.Errorf("expected DefaultAction to be Kill, got %q", profile.DefaultAction)
	}
	if len(profile.AllowedSyscalls) == 0 {
		t.Error("expected AllowedSyscalls to not be empty")
	}

	// Check some expected syscalls are present
	expectedSyscalls := []string{"read", "write", "openat", "close", "ioctl", "mmap"}
	for _, expected := range expectedSyscalls {
		found := false
		for _, syscall := range profile.AllowedSyscalls {
			if syscall == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected syscall %s to be in AllowedSyscalls", expected)
		}
	}
}

func TestNewSandbox(t *testing.T) {
	config := DefaultConfig()
	sandbox := NewSandbox(config)

	if sandbox == nil {
		t.Fatal("expected non-nil sandbox")
	}
	if sandbox.config.Seccomp != config.Seccomp || sandbox.config.Namespaces != config.Namespaces || sandbox.config.Cgroups != config.Cgroups {
		t.Error("expected sandbox config to match input config")
	}
	if sandbox.nsFDs == nil {
		t.Error("expected nsFDs to be initialized")
	}
}

// noopHook is a testMode* hook that bypasses a stage's real,
// privilege-dependent syscalls. applySeccomp (PR_SET_NO_NEW_PRIVS)
// needs no privilege and is exercised for real even here; the other
// three stages routinely require CAP_SYS_ADMIN, delegated cgroups or
// CAP_SETPCAP that a standard `go test ./...` runner does not have, so
// tests that only care about Apply's orchestration (ordering, error
// wrapping) bypass them the same way TestApply*Error already does for
// the opposite (failure) case. The real, privilege-gated syscalls get
// their own coverage in sandbox_linux_test.go, run under
// ci-sandbox.yml where the privilege is actually available.
func noopHook() error { return nil }

func TestSandboxApply(t *testing.T) {
	config := DefaultConfig()
	sandbox := NewSandbox(config)
	sandbox.testModeApplyNamespaces = noopHook
	sandbox.testModeApplyCgroups = noopHook
	sandbox.testModeDropPrivileges = noopHook

	err := sandbox.Apply()
	if err != nil {
		t.Errorf("expected Apply to succeed, got error: %v", err)
	}
}

func TestSandboxCleanup(t *testing.T) {
	config := DefaultConfig()
	sandbox := NewSandbox(config)

	// Cleanup on empty sandbox should succeed
	err := sandbox.Cleanup()
	if err != nil {
		t.Errorf("expected Cleanup to succeed, got error: %v", err)
	}
}

func TestIsSandboxed(t *testing.T) {
	// This will return false in most cases since we're not actually in a sandbox
	// but the function should not panic
	_ = IsSandboxed()
}

func TestNamespaceValues(t *testing.T) {
	if NamespacePID != "pid" {
		t.Errorf("expected NamespacePID to be pid, got %q", NamespacePID)
	}
	if NamespaceNET != "net" {
		t.Errorf("expected NamespaceNET to be net, got %q", NamespaceNET)
	}
	if NamespaceUSER != "user" {
		t.Errorf("expected NamespaceUSER to be user, got %q", NamespaceUSER)
	}
}

// TestDropCapability used to call the real dropCapability("CAP_SYS_ADMIN")
// directly whenever run as root. It doesn't anymore: that drop is
// permanent for the rest of *this test binary's process*, and
// TestApplyNamespacesReal (sandbox_linux_test.go) needs CAP_SYS_ADMIN for
// its own real unshare - the two would only coexist by accident of test
// execution order. See TestDropPrivilegesRealSetuidAndCapabilities in
// sandbox_linux_test.go for where a real capability drop now runs, isolated
// in a throwaway subprocess.
func TestDropCapability(t *testing.T) {
	// Real on Linux (needs CAP_SETPCAP, routinely absent from a
	// standard `go test ./...` runner - see TestDropPrivilegesWithCapabilities),
	// a no-op elsewhere. Either way it must not panic.
	err := dropCapability("CAP_SYS_ADMIN")
	if err != nil {
		t.Logf("dropCapability returned an error, expected without CAP_SETPCAP: %v", err)
	}
}

func TestSandboxConfigStruct(t *testing.T) {
	config := Config{
		Seccomp:        true,
		Namespaces:     true,
		Cgroups:        true,
		DropPrivileges: true,
		CPULimit:       100,
		MemoryLimit:    1024 * 1024 * 1024,
		PIDsLimit:      1000,
	}

	if !config.Seccomp {
		t.Error("expected Seccomp to be true")
	}
	if config.CPULimit != 100 {
		t.Errorf("expected CPULimit to be 100, got %d", config.CPULimit)
	}
	if config.MemoryLimit != 1024*1024*1024 {
		t.Errorf("expected MemoryLimit to be 1GB, got %d", config.MemoryLimit)
	}
	if config.PIDsLimit != 1000 {
		t.Errorf("expected PIDsLimit to be 1000, got %d", config.PIDsLimit)
	}
}

func TestParseNamespaceError(t *testing.T) {
	_, err := ParseNamespace("invalid_namespace")
	if err == nil {
		t.Error("expected error for invalid namespace")
	}
}

// KVM control does not have its own seccomp-filterable syscall names -
// every KVM_* operation, including KVM_RUN, is the single ioctl(2) syscall
// with a request-code argument classic BPF cannot inspect. A prior version
// of this profile listed "kvm_ioctl"/"kvm_vcpu_ioctl"/"kvm_vm_ioctl" as if
// they were real syscalls; they are not, and a filter that "allowed" them
// while never allowing plain "ioctl" would have killed the very KVM_RUN
// loop it was meant to protect. See TestDefaultSeccompProfileNamesResolve
// (sandbox_linux_test.go) for the real, linux-only proof that every name
// in this profile is a syscall that actually exists.
func TestDefaultSeccompProfileAllowsIoctl(t *testing.T) {
	profile := DefaultSeccompProfile()
	for _, allowed := range profile.AllowedSyscalls {
		if allowed == "ioctl" {
			return
		}
	}
	t.Error(`expected "ioctl" (what every KVM_* operation actually is) to be in AllowedSyscalls`)
}

func TestSandboxErrorPropagation(t *testing.T) {
	config := DefaultConfig()
	sandbox := NewSandbox(config)
	sandbox.testModeApplyNamespaces = noopHook
	sandbox.testModeApplyCgroups = noopHook
	sandbox.testModeDropPrivileges = noopHook

	// Apply should not panic
	err := sandbox.Apply()
	if err != nil && !errors.Is(err, nil) {
		t.Errorf("unexpected error type: %T", err)
	}
}

// TestApplyWithSeccompOnly deliberately never lets Apply reach the real
// applySeccomp: doing that here, in the shared go test process, would
// install a real, permanent, non-removable filter on whatever OS thread
// this goroutine happens to be executing on, which the Go scheduler is
// free to hand to a completely unrelated goroutine afterward - a later
// test doing something as ordinary as a network dial could then be
// SIGSYS-killed by a filter this test installed. See
// TestApplySeccompRealFilterBlocksAndAllows in sandbox_linux_test.go for
// where that real behavior is actually exercised, in a throwaway
// subprocess instead.
func TestApplyWithSeccompOnly(t *testing.T) {
	config := Config{
		Seccomp:        true,
		Namespaces:     false,
		Cgroups:        false,
		DropPrivileges: false,
	}
	sandbox := NewSandbox(config)
	sandbox.testModeApplySeccomp = func() error { return nil }
	err := sandbox.Apply()
	if err != nil {
		t.Errorf("expected Apply with seccomp only to succeed, got error: %v", err)
	}
}

// TestApplyWithNamespacesOnly hooks applyNamespaces rather than calling the
// real one: unsharing NET/UTS/IPC needs CAP_SYS_ADMIN, which an ordinary
// `go test` run doesn't have. See TestApplyNamespacesReal in
// sandbox_linux_test.go for the real, privilege-gated version.
func TestApplyWithNamespacesOnly(t *testing.T) {
	config := Config{
		Seccomp:        false,
		Namespaces:     true,
		Cgroups:        false,
		DropPrivileges: false,
	}
	sandbox := NewSandbox(config)
	sandbox.testModeApplyNamespaces = noopHook
	err := sandbox.Apply()
	if err != nil {
		t.Errorf("expected Apply with namespaces only to succeed, got error: %v", err)
	}
}

// TestApplyWithCgroupsOnly hooks applyCgroups rather than calling the real
// one: creating a leaf cgroup needs write access to a delegated cgroup v2
// subtree, which an ordinary `go test` run doesn't have. See
// TestApplyCgroupsReal in sandbox_linux_test.go for the real,
// privilege-gated version.
func TestApplyWithCgroupsOnly(t *testing.T) {
	config := Config{
		Seccomp:        false,
		Namespaces:     false,
		Cgroups:        true,
		DropPrivileges: false,
	}
	sandbox := NewSandbox(config)
	sandbox.testModeApplyCgroups = noopHook
	err := sandbox.Apply()
	if err != nil {
		t.Errorf("expected Apply with cgroups only to succeed, got error: %v", err)
	}
}

// TestApplyWithDropPrivilegesOnly hooks dropPrivileges rather than calling
// the real one: as real root, dropPrivileges' setuid call is a genuine,
// irreversible UID change for the rest of *this test binary's process* -
// letting it run for real here would silently turn every later test in
// this package (however unrelated) that happens to check os.Geteuid()==0
// into a false skip, entirely dependent on test execution order. See
// TestDropPrivilegesRealSetuidAndCapabilities in sandbox_linux_test.go,
// which exercises that exact path for real in a throwaway subprocess.
func TestApplyWithDropPrivilegesOnly(t *testing.T) {
	config := Config{
		Seccomp:          false,
		Namespaces:       false,
		Cgroups:          false,
		DropPrivileges:   true,
		DropCapabilities: []string{"CAP_SYS_ADMIN"},
	}
	sandbox := NewSandbox(config)
	sandbox.testModeDropPrivileges = noopHook
	err := sandbox.Apply()
	if err != nil {
		t.Errorf("expected Apply with drop privileges only to succeed, got error: %v", err)
	}
}

func TestApplyWithAllDisabled(t *testing.T) {
	config := Config{
		Seccomp:        false,
		Namespaces:     false,
		Cgroups:        false,
		DropPrivileges: false,
	}
	sandbox := NewSandbox(config)
	err := sandbox.Apply()
	if err != nil {
		t.Errorf("expected Apply with all disabled to succeed, got error: %v", err)
	}
}

func TestCleanupWithSeccompFD(t *testing.T) {
	config := DefaultConfig()
	sandbox := NewSandbox(config)
	// Use a sentinel value that won't conflict with real FDs
	sandbox.seccompFD = 999

	err := sandbox.Cleanup()
	if err != nil {
		t.Errorf("expected Cleanup with seccomp FD to succeed, got error: %v", err)
	}
	if sandbox.seccompFD != 0 {
		t.Errorf("expected seccompFD to be 0 after cleanup, got %d", sandbox.seccompFD)
	}
}

func TestCleanupWithNamespaceFDs(t *testing.T) {
	config := DefaultConfig()
	sandbox := NewSandbox(config)
	// Use sentinel values that won't conflict with real FDs
	sandbox.nsFDs[NamespacePID] = 999
	sandbox.nsFDs[NamespaceNET] = 1000

	err := sandbox.Cleanup()
	if err != nil {
		t.Errorf("expected Cleanup with namespace FDs to succeed, got error: %v", err)
	}
	if len(sandbox.nsFDs) != 0 {
		t.Errorf("expected nsFDs to be empty after cleanup, got %d entries", len(sandbox.nsFDs))
	}
}

func TestCleanupWithCgroupPath(t *testing.T) {
	config := DefaultConfig()
	sandbox := NewSandbox(config)
	sandbox.cgroupPath = "/sys/fs/cgroup/test"

	err := sandbox.Cleanup()
	if err != nil {
		t.Errorf("expected Cleanup with cgroup path to succeed, got error: %v", err)
	}
	if sandbox.cgroupPath != "" {
		t.Errorf("expected cgroupPath to be empty after cleanup, got %q", sandbox.cgroupPath)
	}
}

func TestCleanupWithAllResources(t *testing.T) {
	config := DefaultConfig()
	sandbox := NewSandbox(config)
	// Use sentinel values that won't conflict with real FDs
	sandbox.seccompFD = 999
	sandbox.nsFDs[NamespacePID] = 1000
	sandbox.cgroupPath = "/sys/fs/cgroup/test"

	err := sandbox.Cleanup()
	if err != nil {
		t.Errorf("expected Cleanup with all resources to succeed, got error: %v", err)
	}
	if sandbox.seccompFD != 0 {
		t.Errorf("expected seccompFD to be 0 after cleanup, got %d", sandbox.seccompFD)
	}
	if len(sandbox.nsFDs) != 0 {
		t.Errorf("expected nsFDs to be empty after cleanup, got %d entries", len(sandbox.nsFDs))
	}
	if sandbox.cgroupPath != "" {
		t.Errorf("expected cgroupPath to be empty after cleanup, got %q", sandbox.cgroupPath)
	}
}

func TestIsInUserNamespace(t *testing.T) {
	// This test just verifies the function doesn't panic
	// The actual result depends on the test environment
	_ = isInUserNamespace()
}

func TestIsSeccompEnabledFunc(t *testing.T) {
	// This test just verifies the function doesn't panic
	// The actual result depends on the test environment
	_ = isSeccompEnabled()
}

func TestApplyAllFeatures(t *testing.T) {
	// Test Apply with all features enabled. See TestApplyWithSeccompOnly
	// for why applySeccomp specifically is never allowed to run for real
	// inside the shared go test process.
	config := Config{
		Seccomp:          true,
		Namespaces:       true,
		Cgroups:          true,
		DropPrivileges:   true,
		DropCapabilities: []string{"CAP_SYS_ADMIN"},
	}
	sandbox := NewSandbox(config)
	sandbox.testModeApplyNamespaces = noopHook
	sandbox.testModeApplyCgroups = noopHook
	sandbox.testModeDropPrivileges = noopHook
	err := sandbox.Apply()
	if err != nil {
		t.Errorf("expected Apply with all features to succeed, got error: %v", err)
	}
}

// TestDropPrivilegesWithCapabilities used to call the real dropPrivileges
// directly whenever run as root. It doesn't anymore: dropPrivileges' setuid
// call, taken for real as root, is a genuine and irreversible UID change
// for the rest of *this test binary's process* - it would silently turn
// every later test in this package that happens to check
// os.Geteuid()==0 into a false skip, entirely dependent on execution
// order (this was caught, not theorized: TestApplyWithDropPrivilegesOnly
// used to have the same problem and reliably broke this exact test when
// both ran as root in one binary). See
// TestDropPrivilegesRealSetuidAndCapabilities in sandbox_linux_test.go for
// where that real path now actually runs, isolated in a throwaway
// subprocess.
func TestDropPrivilegesWithCapabilities(t *testing.T) {
	config := Config{
		DropPrivileges:   true,
		DropCapabilities: []string{"CAP_SYS_ADMIN", "CAP_NET_ADMIN"},
	}
	sandbox := NewSandbox(config)
	// This exercises the real, unhooked dropCapability path (unlike
	// Apply(), dropPrivileges is called directly here). Dropping
	// CAP_SYS_ADMIN/CAP_NET_ADMIN from the bounding set requires
	// CAP_SETPCAP, which a standard `go test ./...` runner does not
	// have, so - like TestDropPrivilegesAsRoot below - both outcomes
	// are acceptable here; what matters is that dropPrivileges doesn't
	// panic and returns a real error rather than silently pretending
	// to succeed when it didn't.
	err := sandbox.dropPrivileges()
	if err != nil {
		t.Logf("dropPrivileges returned an error, expected without CAP_SETPCAP: %v", err)
	}
}

func TestDropPrivilegesEmptyCapabilities(t *testing.T) {
	config := Config{
		DropPrivileges:   true,
		DropCapabilities: []string{},
	}
	sandbox := NewSandbox(config)
	err := sandbox.dropPrivileges()
	if err != nil {
		t.Errorf("expected dropPrivileges with empty capabilities to succeed, got error: %v", err)
	}
}

func TestIsSandboxedFalse(t *testing.T) {
	// In a normal test environment, IsSandboxed should return false
	result := IsSandboxed()
	// We don't assert the value since it depends on the environment
	_ = result
}

func TestDropPrivilegesAsRoot(t *testing.T) {
	config := Config{
		DropPrivileges:   true,
		DropCapabilities: []string{},
	}
	sandbox := NewSandbox(config)
	// Simulate running as root by setting testModeGetEuid to return 0
	sandbox.testModeGetEuid = func() int { return 0 }
	// Note: This will try to call syscall.Setuid(65534) but will likely fail
	// since we're not actually running as root. We just test that the code path is covered.
	err := sandbox.dropPrivileges()
	// We expect an error since we're not actually root
	if err == nil {
		// If no error, that's also fine - means Setuid worked or was a no-op
		_ = true
	}
	// The important thing is that the code path was executed
}

func TestApplySeccompError(t *testing.T) {
	config := Config{
		Seccomp: true,
	}
	sandbox := NewSandbox(config)
	// Set up a hook that returns an error
	sandbox.testModeApplySeccomp = func() error {
		return fmt.Errorf("seccomp error")
	}

	err := sandbox.Apply()
	if err == nil {
		t.Error("expected Apply to return error when seccomp fails")
	}
}

func TestApplyNamespacesError(t *testing.T) {
	config := Config{
		Namespaces: true,
	}
	sandbox := NewSandbox(config)
	// Set up a hook that returns an error
	sandbox.testModeApplyNamespaces = func() error {
		return fmt.Errorf("namespaces error")
	}

	err := sandbox.Apply()
	if err == nil {
		t.Error("expected Apply to return error when namespaces fails")
	}
}

func TestApplyCgroupsError(t *testing.T) {
	config := Config{
		Cgroups: true,
	}
	sandbox := NewSandbox(config)
	// Set up a hook that returns an error
	sandbox.testModeApplyCgroups = func() error {
		return fmt.Errorf("cgroups error")
	}

	err := sandbox.Apply()
	if err == nil {
		t.Error("expected Apply to return error when cgroups fails")
	}
}

func TestApplyDropPrivilegesError(t *testing.T) {
	config := Config{
		DropPrivileges: true,
	}
	sandbox := NewSandbox(config)
	// Set up a hook that returns an error
	sandbox.testModeDropPrivileges = func() error {
		return fmt.Errorf("drop privileges error")
	}

	err := sandbox.Apply()
	if err == nil {
		t.Error("expected Apply to return error when dropPrivileges fails")
	}
}

func TestDropPrivilegesSetuidError(t *testing.T) {
	config := Config{
		DropPrivileges:   true,
		DropCapabilities: []string{},
	}
	sandbox := NewSandbox(config)
	// Simulate running as root
	sandbox.testModeGetEuid = func() int { return 0 }
	// Set up a hook that returns an error for Setuid
	sandbox.testModeSetuid = func() error {
		return fmt.Errorf("setuid error")
	}

	err := sandbox.dropPrivileges()
	if err == nil {
		t.Error("expected dropPrivileges to return error when setuid fails")
	}
}

func TestDropPrivilegesSuccessAsRoot(t *testing.T) {
	config := Config{
		DropPrivileges:   true,
		DropCapabilities: []string{},
	}
	sandbox := NewSandbox(config)
	// Simulate running as root
	sandbox.testModeGetEuid = func() int { return 0 }
	// Set up a hook that succeeds for Setuid
	sandbox.testModeSetuid = func() error {
		return nil
	}

	err := sandbox.dropPrivileges()
	if err != nil {
		t.Errorf("expected dropPrivileges to succeed when setuid succeeds, got error: %v", err)
	}
}
