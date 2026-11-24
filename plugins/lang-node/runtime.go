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
	canonicalNodeName  = "node"
	nodeRuntimeRelPath = ".platform-factory/deps/node/runtime"
)

func runRuntime(args []string) error {
	root, imageRoot, interpreterHint, err := langplugin.ParseRuntimeFlags(args)
	if err != nil {
		return err
	}
	interpreterImagePath, err := findNodeInterpreter(imageRoot, interpreterHint)
	if err != nil {
		return err
	}
	interpreterDiskPath := filepath.Join(imageRoot, filepath.FromSlash(strings.TrimPrefix(interpreterImagePath, "/")))

	dependencies, err := langplugin.ResolveRuntimeClosure(imageRoot, interpreterDiskPath, interpreterImagePath, nil, false, os.Stderr)
	if err != nil {
		return fmt.Errorf("resolve runtime dependencies of %s: %w", interpreterImagePath, err)
	}

	stageDir := filepath.Join(root, nodeRuntimeRelPath)
	if err := os.RemoveAll(stageDir); err != nil {
		return err
	}
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return err
	}

	stagedInterpreter := filepath.Join(stageDir, canonicalNodeName)
	if err := langplugin.CopyRuntimeFile(interpreterDiskPath, stagedInterpreter); err != nil {
		return fmt.Errorf("stage interpreter: %w", err)
	}
	if err := os.Chmod(stagedInterpreter, 0o755); err != nil {
		return fmt.Errorf("chmod interpreter: %w", err)
	}

	manifest := langplugin.RuntimeManifest{
		Runtime: langplugin.RelRuntimePath(root, stagedInterpreter),
	}
	for index, dependency := range dependencies {
		stagedName := fmt.Sprintf("lib-%d-%s", index, filepath.Base(dependency.ImagePath))
		stagedPath := filepath.Join(stageDir, stagedName)
		if err := langplugin.CopyRuntimeFile(dependency.DiskPath, stagedPath); err != nil {
			return fmt.Errorf("stage %s: %w", dependency.ImagePath, err)
		}
		destination := dependency.ImagePath
		if dependency.OriginRelative {
			destination = filepath.ToSlash(filepath.Join("/runtime", dependency.OriginRelativeFromInterpreter))
		}
		manifest.Include = append(manifest.Include, langplugin.RuntimeIncludeEntry{
			Source:      langplugin.RelRuntimePath(root, stagedPath),
			Destination: destination,
			Category:    "toolchain",
		})
	}
	return langplugin.WriteRuntimeManifest(manifest)
}

func findNodeInterpreter(imageRoot, hint string) (string, error) {
	if hint != "" {
		disk := filepath.Join(imageRoot, filepath.FromSlash(strings.TrimPrefix(hint, "/")))
		if info, err := os.Stat(disk); err == nil && info.Mode().IsRegular() {
			return "/" + strings.TrimPrefix(hint, "/"), nil
		}
		return "", fmt.Errorf("--interpreter %s not found (or not a regular file) in the pulled image", hint)
	}
	for _, candidate := range []string{"/usr/local/bin/node", "/usr/bin/node"} {
		disk := filepath.Join(imageRoot, filepath.FromSlash(strings.TrimPrefix(candidate, "/")))
		if info, err := os.Stat(disk); err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return "", errors.New("no node binary found under /usr/local/bin/node or /usr/bin/node in the pulled image - pass --interpreter to name one explicitly")
}
