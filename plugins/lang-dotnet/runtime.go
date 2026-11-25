package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CYPT71/platform-factory/sdk/langplugin"
)

const (
	canonicalDotnetName  = "dotnet"
	dotnetRuntimeRelPath = ".platform-factory/deps/dotnet/runtime"
)

// dotnetDlopenLibraryPrefixes are system shared libraries that the .NET CoreCLR
// runtime loads dynamically via dlopen (ICU for globalization and OpenSSL for
// cryptography/TLS) rather than static DT_NEEDED links.
var dotnetDlopenLibraryPrefixes = []string{
	"libicudata.so.",
	"libicui18n.so.",
	"libicuuc.so.",
	"libssl.so.",
	"libcrypto.so.",
}

func runRuntime(args []string) error {
	root, imageRoot, interpreterHint, err := langplugin.ParseRuntimeFlags(args)
	if err != nil {
		return err
	}
	interpreterImagePath, dotnetRootImagePath, err := findDotnetHost(imageRoot, interpreterHint)
	if err != nil {
		return err
	}
	interpreterDiskPath := filepath.Join(imageRoot, filepath.FromSlash(strings.TrimPrefix(interpreterImagePath, "/")))
	dotnetRootDiskPath := filepath.Join(imageRoot, filepath.FromSlash(strings.TrimPrefix(dotnetRootImagePath, "/")))

	hostDirDisk := filepath.Join(dotnetRootDiskPath, "host")
	sharedDirDisk := filepath.Join(dotnetRootDiskPath, "shared")
	for _, required := range []string{hostDirDisk, sharedDirDisk} {
		if info, statErr := os.Stat(required); statErr != nil || !info.IsDir() {
			return fmt.Errorf("expected .NET runtime directory at %s beside %s", required, interpreterImagePath)
		}
	}

	// Fail closed immediately if the dotnet muxer itself has missing dependencies.
	if _, err := langplugin.ResolveRuntimeClosure(imageRoot, interpreterDiskPath, interpreterImagePath, nil, false, os.Stderr); err != nil {
		return fmt.Errorf("resolve runtime dependencies of %s: %w", interpreterImagePath, err)
	}

	var seeds []langplugin.SharedObject
	for _, tree := range []string{hostDirDisk, sharedDirDisk} {
		found, findErr := langplugin.FindSharedObjects(tree, imageRoot)
		if findErr != nil {
			return fmt.Errorf("scan .NET runtime shared objects under %s: %w", tree, findErr)
		}
		seeds = append(seeds, found...)
	}

	// Optional native modules (for example libcoreclrtraceptprovider.so which
	// links against liblttng-ust.so.0 absent in slim runtime images) cannot pass
	// strict OCI ELF validation if staged incomplete. Probe each module
	// independently and exclude any unloadable optional .so from the staged tree.
	excludedModules := map[string]bool{}
	var usableSeeds []langplugin.SharedObject
	for _, seed := range seeds {
		if probeErr := langplugin.ProbeSharedObject(imageRoot, seed); probeErr != nil {
			fmt.Fprintf(os.Stderr, "platform-factory-lang-dotnet: runtime: excluding %s from the staged runtime (%v)\n", seed.ImagePath, probeErr)
			excludedModules[seed.DiskPath] = true
			continue
		}
		usableSeeds = append(usableSeeds, seed)
	}

	rawDlopenLibs, err := langplugin.FindLibrariesByPrefix(imageRoot, dotnetDlopenLibraryPrefixes)
	if err != nil {
		return fmt.Errorf("scan .NET dlopen libraries: %w", err)
	}
	var dlopenLibs []langplugin.SharedObject
	for _, lib := range rawDlopenLibs {
		if probeErr := langplugin.ProbeSharedObject(imageRoot, lib); probeErr != nil {
			fmt.Fprintf(os.Stderr, "platform-factory-lang-dotnet: runtime: skipping optional dlopen library %s (%v)\n", lib.ImagePath, probeErr)
			continue
		}
		dlopenLibs = append(dlopenLibs, lib)
	}

	allSeeds := append(append([]langplugin.SharedObject(nil), usableSeeds...), dlopenLibs...)

	dependencies, err := langplugin.ResolveRuntimeClosure(imageRoot, interpreterDiskPath, interpreterImagePath, allSeeds, false, os.Stderr)
	if err != nil {
		return fmt.Errorf("resolve runtime dependencies of %s: %w", interpreterImagePath, err)
	}

	stageDir := filepath.Join(root, dotnetRuntimeRelPath)
	if err := os.RemoveAll(stageDir); err != nil {
		return err
	}
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return err
	}

	stagedHost := filepath.Join(stageDir, canonicalDotnetName)
	if err := langplugin.CopyRuntimeFile(interpreterDiskPath, stagedHost); err != nil {
		return fmt.Errorf("stage dotnet host: %w", err)
	}
	if err := os.Chmod(stagedHost, 0o755); err != nil {
		return fmt.Errorf("chmod dotnet host: %w", err)
	}

	manifest := langplugin.RuntimeManifest{
		Runtime: langplugin.RelRuntimePath(root, stagedHost),
	}

	// Stage host/ and shared/ directly under /runtime/ so the relocated
	// /runtime/dotnet muxer and DOTNET_ROOT=/runtime resolve fxr and
	// Microsoft.NETCore.App assemblies without host path coupling.
	for _, subdir := range []string{"host", "shared"} {
		stagedTree := filepath.Join(stageDir, subdir)
		if err := langplugin.CopyRuntimeTree(filepath.Join(dotnetRootDiskPath, subdir), stagedTree, excludedModules); err != nil {
			return fmt.Errorf("stage .NET %s tree: %w", subdir, err)
		}
		manifest.Include = append(manifest.Include, langplugin.RuntimeIncludeEntry{
			Source:      langplugin.RelRuntimePath(root, stagedTree),
			Destination: "/runtime/" + subdir,
			Category:    "toolchain",
		})
	}

	stagedDestinations := map[string]bool{}
	libIndex := 0
	stageLib := func(diskPath, destination string) error {
		if stagedDestinations[destination] {
			return nil
		}
		stagedDestinations[destination] = true
		stagedName := fmt.Sprintf("lib-%d-%s", libIndex, filepath.Base(destination))
		libIndex++
		stagedPath := filepath.Join(stageDir, stagedName)
		if err := langplugin.CopyRuntimeFile(diskPath, stagedPath); err != nil {
			return fmt.Errorf("stage %s: %w", destination, err)
		}
		manifest.Include = append(manifest.Include, langplugin.RuntimeIncludeEntry{
			Source:      langplugin.RelRuntimePath(root, stagedPath),
			Destination: destination,
			Category:    "toolchain",
		})
		return nil
	}

	for _, lib := range dlopenLibs {
		if err := stageLib(lib.DiskPath, lib.ImagePath); err != nil {
			return err
		}
	}
	for _, dependency := range dependencies {
		destination := dependency.ImagePath
		if dependency.OriginRelative {
			destination = filepath.ToSlash(filepath.Join("/runtime", dependency.OriginRelativeFromInterpreter))
		}
		// Skip libraries already staged inside /runtime/host or /runtime/shared.
		if strings.HasPrefix(dependency.ImagePath, dotnetRootImagePath+"/host/") ||
			strings.HasPrefix(dependency.ImagePath, dotnetRootImagePath+"/shared/") {
			continue
		}
		if err := stageLib(dependency.DiskPath, destination); err != nil {
			return err
		}
	}

	return langplugin.WriteRuntimeManifest(manifest)
}

func findDotnetHost(imageRoot, hint string) (interpreterImagePath, dotnetRootImagePath string, err error) {
	if hint != "" {
		clean := "/" + strings.TrimPrefix(hint, "/")
		disk := filepath.Join(imageRoot, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
		if info, statErr := os.Stat(disk); statErr == nil && info.Mode().IsRegular() {
			return clean, filepath.ToSlash(filepath.Dir(clean)), nil
		}
		return "", "", fmt.Errorf("--interpreter %s not found (or not a regular file) in the pulled image", hint)
	}
	for _, rootDir := range []string{"/usr/share/dotnet", "/usr/local/share/dotnet", "/opt/dotnet"} {
		candidate := rootDir + "/dotnet"
		disk := filepath.Join(imageRoot, filepath.FromSlash(strings.TrimPrefix(candidate, "/")))
		if info, statErr := os.Stat(disk); statErr == nil && info.Mode().IsRegular() {
			return candidate, rootDir, nil
		}
	}
	return "", "", errors.New("no dotnet host found under /usr/share/dotnet, /usr/local/share/dotnet, or /opt/dotnet in the pulled image - pass --interpreter to name one explicitly")
}
