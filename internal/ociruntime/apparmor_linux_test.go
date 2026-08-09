package ociruntime

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// loadTestApparmorProfile loads a minimal complain-mode profile under name,
// skipping the test if this host can't (no AppArmor, not root, or no
// apparmor_parser), and schedules it to be unloaded on cleanup.
func loadTestApparmorProfile(t *testing.T, name string) {
	t.Helper()
	if !appArmorEnabled() {
		t.Skip("AppArmor is not enabled on this host")
	}
	if os.Geteuid() != 0 {
		t.Skip("loading a test profile requires root")
	}
	parser, err := exec.LookPath("apparmor_parser")
	if err != nil {
		t.Skip("apparmor_parser is not installed")
	}
	profile := "profile " + name + " flags=(complain) {\n}\n"
	path := filepath.Join(t.TempDir(), "test.profile")
	if err := os.WriteFile(path, []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(parser, "-r", path).CombinedOutput(); err != nil {
		t.Fatalf("load test profile: %v: %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command(parser, "-R", path).Run()
	})
}

func TestValidAppArmorProfileName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"", false},
		{"docker-default", true},
		{"secure-oci/example", true},
		{"has\nnewline", false},
		{"has\x00nul", false},
	}
	for _, c := range cases {
		if got := validAppArmorProfileName(c.name); got != c.want {
			t.Errorf("validAppArmorProfileName(%q)=%v, want %v", c.name, got, c.want)
		}
	}
}

func TestApparmorProfileListedIn(t *testing.T) {
	listing := "docker-default (enforce)\nunconfined\nsecure-oci-example (complain)\n"
	cases := []struct {
		name string
		want bool
	}{
		{"docker-default", true},
		{"secure-oci-example", true},
		{"docker-default-extra", false}, // must not match on a bare prefix
		{"unconfined", false},           // no "(mode)" suffix in this fixture: not a loaded profile line
		{"missing", false},
	}
	for _, c := range cases {
		got, err := apparmorProfileListedIn(strings.NewReader(listing), c.name)
		if err != nil {
			t.Fatalf("apparmorProfileListedIn(%q): %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("apparmorProfileListedIn(%q)=%v, want %v", c.name, got, c.want)
		}
	}
}

// This asserts against the real /sys/kernel/security/apparmor/profiles file
// when the host has one, rather than a fake: a name this test invents can
// never legitimately be loaded, so the assertion holds regardless of what
// else is actually loaded on whatever machine runs this.
func TestAppArmorProfileLoadedReportsUnloadedForUnknownProfile(t *testing.T) {
	if !appArmorEnabled() {
		t.Skip("AppArmor is not enabled on this host")
	}
	// The kernel's securityfs profiles listing enforces its own access
	// check beyond the plain, misleadingly world-readable file mode -
	// reading it as a non-root, unconfined process fails closed with
	// EPERM, not an empty list.
	if os.Geteuid() != 0 {
		t.Skip("reading the loaded-profiles list requires root")
	}
	loaded, err := appArmorProfileLoaded("secure-oci-test-definitely-unloaded-profile")
	if err != nil {
		t.Fatal(err)
	}
	if loaded {
		t.Fatal("invented profile name reported as loaded")
	}
}

// Loading a profile requires CAP_MAC_ADMIN (root) and the apparmor_parser
// userspace tool - this runtime doesn't need it (it never loads profile
// text, see this file's package doc comment), but proving
// applyApparmorProfile's changeprofile write actually transitions the
// calling thread does. Skips everywhere that combination isn't available,
// which is everywhere except a real, privileged CI runner.
func TestApplyApparmorProfileTransitionsCurrentThread(t *testing.T) {
	name := "secure-oci-apparmor-test"
	loadTestApparmorProfile(t, name)

	loaded, err := appArmorProfileLoaded(name)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded {
		t.Fatal("apparmor_parser reported success but the profile isn't listed as loaded")
	}

	if err := applyApparmorProfile(name); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile("/proc/thread-self/attr/current")
	if err != nil {
		current, err = os.ReadFile("/proc/thread-self/attr/apparmor/current")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(current), name) {
		t.Fatalf("current apparmor attr=%q, want prefix %q", current, name)
	}
}

// testBundleWithProcess loads testBundle(t)'s config.json, applies mutate to
// it, and rewrites it - a smaller variant of the mutation pattern
// TestLoadConfigRejectsEscapeAndUnsupportedSemantics already uses in
// runtime_linux_test.go, scoped to just the Process field these tests care
// about.
func testBundleWithProcess(t *testing.T, mutate func(*Process)) string {
	t.Helper()
	bundle := testBundle(t)
	data, err := os.ReadFile(filepath.Join(bundle, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	mutate(&config.Process)
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestLoadConfigRejectsSelinuxLabel(t *testing.T) {
	bundle := testBundleWithProcess(t, func(p *Process) {
		p.SelinuxLabel = "system_u:system_r:container_t:s0"
	})
	if _, err := LoadConfig(bundle); err == nil || !strings.Contains(err.Error(), "SELinux labels are not yet supported") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadConfigRejectsInvalidApparmorProfileName(t *testing.T) {
	bundle := testBundleWithProcess(t, func(p *Process) {
		p.ApparmorProfile = "has\nnewline"
	})
	if _, err := LoadConfig(bundle); err == nil || !strings.Contains(err.Error(), "invalid apparmor profile name") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadConfigRejectsUnloadedApparmorProfile(t *testing.T) {
	if !appArmorEnabled() {
		t.Skip("AppArmor is not enabled on this host")
	}
	if os.Geteuid() != 0 {
		t.Skip("reading the loaded-profiles list requires root")
	}
	bundle := testBundleWithProcess(t, func(p *Process) {
		p.ApparmorProfile = "secure-oci-test-definitely-unloaded-profile"
	})
	if _, err := LoadConfig(bundle); err == nil || !strings.Contains(err.Error(), "is not loaded on this host") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadConfigAcceptsLoadedApparmorProfile(t *testing.T) {
	name := "secure-oci-apparmor-loadconfig-test"
	loadTestApparmorProfile(t, name)
	bundle := testBundleWithProcess(t, func(p *Process) {
		p.ApparmorProfile = name
	})
	loaded, err := LoadConfig(bundle)
	if err != nil {
		t.Fatalf("loaded apparmor profile rejected: %v", err)
	}
	if loaded.Process.ApparmorProfile != name {
		t.Fatalf("ApparmorProfile=%q, want %q", loaded.Process.ApparmorProfile, name)
	}
}
