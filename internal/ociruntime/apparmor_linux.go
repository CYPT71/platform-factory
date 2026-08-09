package ociruntime

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// This runtime never compiles or loads AppArmor profile text itself - the
// real userspace apparmor_parser tool is what does that, invoked by
// whichever operator or container engine is responsible for a given
// profile's lifecycle, the same way runc and containerd's own CRI plugin
// both only ever reference profiles by name. What lives here is the piece
// that's genuinely this runtime's job: refuse a profile that was never
// loaded rather than silently booting a MicroVM supervisor unconfined, and
// apply the one that was.

// appArmorProfileNamePattern rejects control characters (including the
// newline aa_change_profile's own kernel-side command grammar splits on) and
// an empty name; AppArmor profile names are otherwise permissive (slashes
// delimit namespaces/hats).
var appArmorProfileNamePattern = regexp.MustCompile(`^[^\x00-\x1f]+$`)

func validAppArmorProfileName(name string) bool {
	return name != "" && appArmorProfileNamePattern.MatchString(name)
}

// appArmorEnabled reports whether the host kernel has AppArmor active,
// mirroring the exact check containerd's own CRI plugin uses to decide
// whether to generate an AppArmor SpecOpts at all
// (pkg/apparmor.hostSupports): the securityfs mount exists and the
// "enabled" module parameter reads "Y". A profile can only ever be loaded,
// and change_profile can only ever succeed, when this is true.
func appArmorEnabled() bool {
	if _, err := os.Stat("/sys/kernel/security/apparmor"); err != nil {
		return false
	}
	buf, err := os.ReadFile("/sys/module/apparmor/parameters/enabled")
	return err == nil && len(buf) > 0 && buf[0] == 'Y'
}

// appArmorProfileLoaded reports whether name is loaded into the kernel,
// parsing /sys/kernel/security/apparmor/profiles - the same file and
// "<name> (<mode>)" line format containerd's own equivalent check parses
// (internal/cri/sputil.appArmorProfileExists).
func appArmorProfileLoaded(name string) (bool, error) {
	f, err := os.Open("/sys/kernel/security/apparmor/profiles")
	if err != nil {
		return false, fmt.Errorf("oci runtime: read loaded apparmor profiles: %w", err)
	}
	defer f.Close()
	loaded, err := apparmorProfileListedIn(f, name)
	if err != nil {
		return false, fmt.Errorf("oci runtime: read loaded apparmor profiles: %w", err)
	}
	return loaded, nil
}

// apparmorProfileListedIn is the pure parsing half of appArmorProfileLoaded,
// separated out so it can be tested against a fake profiles listing without
// a real AppArmor-enabled kernel.
func apparmorProfileListedIn(r io.Reader, name string) (bool, error) {
	prefix := name + " ("
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), prefix) {
			return true, nil
		}
	}
	return false, scanner.Err()
}

// procAttrPaths returns the candidate /proc/thread-self/attr/... paths for
// attr, newest-kernel-first: the AppArmor-namespaced attr directory Linux
// 4.14+ exposes, falling back to the classic shared one for older kernels
// (the same two-path fallback runc's own apparmor package uses). Only the
// calling thread may ever write its own attr file, hence thread-self rather
// than self: on a multi-threaded Go binary, self is not well-defined for
// writes to a per-thread LSM attribute.
var procAttrPaths = func(attr string) []string {
	return []string{
		"/proc/thread-self/attr/apparmor/" + attr,
		"/proc/thread-self/attr/" + attr,
	}
}

// applyApparmorProfile immediately (not on the next exec) transitions the
// calling OS thread into the named AppArmor profile via the kernel's
// documented setprocattr "changeprofile" command
// (security/apparmor/lsm.c:do_setattr), the same primitive libapparmor's
// aa_change_profile() wraps. It must be called before spawning additional
// OS threads that should also end up confined and before touching anything
// the profile is meant to restrict: unlike an onexec transition, it has no
// effect on work already in flight, only on what the thread does from this
// call onward.
func applyApparmorProfile(name string) error {
	if name == "" {
		return nil
	}
	command := []byte("changeprofile " + name)
	var lastErr error
	for _, path := range procAttrPaths("current") {
		if err := os.WriteFile(path, command, 0); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no apparmor attr interface found")
	}
	return fmt.Errorf("oci runtime: apply apparmor profile %q: %w", name, lastErr)
}
