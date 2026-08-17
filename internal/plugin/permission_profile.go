package plugin

// PermissionProfile is the resource ceiling applied to a sandboxed plugin
// subprocess, resolved from the plugin's own declared Manifest.Family - a
// so resolving a profile from it needs no change to the signed manifest
// format itself.
//
// MemoryMiB deliberately has no single safe cross-family default: a bare Go
// runtime needs on the order of 1 GiB of virtual address space just to
// boot regardless of actual heap use (confirmed empirically - see
// wrapWithPluginSandbox's doc comment), and third-party plugins in other
// languages have their own, different bootstrap footprints. The exact floor
// is not a clean threshold, either: re-measured directly against this
// package's own sandboxprobe test binary under RLIMIT_AS via `ulimit -v`,
// 512/640/768 MiB failed outright ("failed to reserve page summary
// memory"), 896 MiB still failed ("out of memory allocating heap arena
// map"), 1024 MiB happened to succeed but 1152 MiB (higher!) failed with a
// SIGABRT part-way through pthread_create - thread-stack reservation makes
// the boundary noisy, not a hard cutoff, so a profile sitting close to
// "the smallest value that happened to work once" would be flaky. Every
// profile below was instead chosen with real margin above that observed
// danger zone (confirmed stable over 5 repeated trials each at 2048 and
// 4096 MiB); MemoryMiB == 0 (the unknown-family fallback) means "no
// RLIMIT_AS applied", exactly matching this package's behavior before
// per-family profiles existed, so a plugin that declares no family or an
// unrecognized one is not newly put at risk of a guessed ceiling breaking
// it.
type PermissionProfile struct {
	// MemoryMiB bounds virtual address space (RLIMIT_AS) in MiB; 0 means
	// unlimited.
	MemoryMiB uint64
	// CPUSeconds bounds CPU time (RLIMIT_CPU) in seconds; 0 means
	// unlimited.
	CPUSeconds uint64
	// Processes bounds process/thread count (RLIMIT_NPROC) for this
	// plugin's own user namespace - see applyResourceLimits' doc comment
	// on why this is safe to scope per plugin rather than host-wide.
	Processes uint64
}

// defaultPermissionProfile is applied when a plugin's manifest omits
// Family, or declares one this host does not recognize - the same
// behavior this package had before per-family profiles existed.
var defaultPermissionProfile = PermissionProfile{CPUSeconds: 60, Processes: 64}

// permissionProfiles holds one entry per known PluginFamily. Every
// MemoryMiB here was re-verified stable (see PermissionProfile's doc
// comment for the measurements), not guessed per family from first
// principles - families expected to run heavier, longer-lived toolchains
// (build, runtime) get more headroom than ones expected to be quick,
// lightweight checks (language detection, deployment manifest rendering).
var permissionProfiles = map[PluginFamily]PermissionProfile{
	PluginFamilyLanguage:   {MemoryMiB: 2048, CPUSeconds: 60, Processes: 64},
	PluginFamilyAnalyzer:   {MemoryMiB: 2048, CPUSeconds: 120, Processes: 64},
	PluginFamilyBuild:      {MemoryMiB: 4096, CPUSeconds: 300, Processes: 128},
	PluginFamilyRuntime:    {MemoryMiB: 4096, CPUSeconds: 60, Processes: 128},
	PluginFamilyDeployment: {MemoryMiB: 2048, CPUSeconds: 120, Processes: 64},
	PluginFamilyCapability: {MemoryMiB: 2048, CPUSeconds: 60, Processes: 64},
}

// permissionProfileFor resolves family to its PermissionProfile, falling
// back to defaultPermissionProfile for "" or any family this host does not
// recognize - never a guessed value, and never a zero-value profile that
// would silently mean "no limits at all" for a family with a real, known
// entry above.
func permissionProfileFor(family PluginFamily) PermissionProfile {
	if profile, ok := permissionProfiles[family]; ok {
		return profile
	}
	return defaultPermissionProfile
}
