//go:build !linux

package sandbox

// applySeccomp, applyNamespaces, applyCgroups and dropCapability are
// no-ops outside Linux: this package's only current caller (the VMM
// host process launched by internal/ociruntime/supervisor_linux.go)
// only builds and runs on linux/amd64, so there is nothing to
// enforce here. These stubs exist so the package - and code that
// merely references sandbox.Config without gating on GOOS - stays
// buildable and testable on the platforms this project develops and
// cross-compiles from.

func (s *Sandbox) applySeccomp() error       { return nil }
func (s *Sandbox) applyStrictSeccomp() error { return nil }
func (s *Sandbox) applyNamespaces() error    { return nil }
func (s *Sandbox) applyCgroups() error       { return nil }

func dropCapability(cap string) error            { return nil }
func dropCapabilityBoundingSet(cap string) error { return nil }
func dropCapabilityCurrentSet(cap string) error  { return nil }

func isInUserNamespace() bool { return false }
func isSeccompEnabled() bool  { return false }

// ProbeSandbox reports that none of this package's real sandboxing
// operations are implemented outside Linux. Details carries a reason
// under each facility's own key (matching sandbox_linux.go's
// ProbeSandbox), not just a generic one - callers like `platform-factory
// doctor` look up the specific facility key for any check that came
// back unavailable, and a missing key there reads as a bug (an
// unavailable check with no explanation) rather than as "not on
// Linux".
func ProbeSandbox() Support {
	const reason = "sandboxing is only implemented on linux"
	return Support{Details: map[string]string{
		"namespaces":               reason,
		"cgroups":                  reason,
		"capability-bounding-drop": reason,
	}}
}
