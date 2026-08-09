package project

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CYPT71/secure-oci-base/internal/oci"
)

func (loaded Loaded) ImageFiles() ([]oci.ExtraFile, error) {
	config := loaded.Config
	includeProject := config.Language != "go" && config.Language != "compiled" && config.Language != "custom"
	if config.IncludeProject != nil {
		includeProject = *config.IncludeProject
	}
	dependencies := append([]Dependency(nil), config.Include...)
	if includeProject {
		dependencies = append([]Dependency{{Source: ".", Destination: "/app", Category: oci.CategoryApplication}}, dependencies...)
	}
	for index := range config.SharedDeps {
		shared := config.SharedDeps[index]
		if shared.Category == "" {
			shared.Category = oci.CategoryDependencies
		}
		dependencies = append(dependencies, shared)
	}
	for _, name := range []string{"shared_deps", "Shared_deps", "shared-deps"} {
		candidate := filepath.Join(loaded.Root, name)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() && !hasSource(dependencies, candidate, loaded) {
			dependencies = append(dependencies, Dependency{
				Source: candidate, Destination: "/app/shared_deps",
				Category: oci.CategoryDependencies,
			})
			break
		}
	}

	output := loaded.Output()
	var files []oci.ExtraFile
	destinations := map[string]bool{}
	for _, dependency := range dependencies {
		source := loaded.Resolve(dependency.Source)
		collected, err := collectTree(source, dependency.Destination, output, dependency.Category)
		if err != nil {
			return nil, fmt.Errorf("collect %s: %w", dependency.Source, err)
		}
		for _, file := range collected {
			if destinations[file.Dest] {
				return nil, fmt.Errorf("duplicate image destination %s", file.Dest)
			}
			destinations[file.Dest] = true
			files = append(files, file)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Dest < files[j].Dest })
	return files, nil
}

func hasSource(dependencies []Dependency, candidate string, loaded Loaded) bool {
	for _, dependency := range dependencies {
		if loaded.Resolve(dependency.Source) == candidate {
			return true
		}
	}
	return false
}

func collectTree(source, destination, output, category string) ([]oci.ExtraFile, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("symbolic links are not accepted")
	}
	if info.Mode().IsRegular() {
		return []oci.ExtraFile{{Source: source, Dest: destination, Mode: int64(info.Mode().Perm()), Category: category}}, nil
	}
	if !info.IsDir() {
		return nil, errors.New("source must be a regular file or directory")
	}
	var result []oci.ExtraFile
	err = filepath.WalkDir(source, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filename == output || strings.HasPrefix(filename, output+string(filepath.Separator)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		relative, err := filepath.Rel(source, filename)
		if err != nil {
			return err
		}
		if filepath.ToSlash(relative) == ".platform-factory/freeze.lock.json" {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic link %s is not accepted", filename)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", filename)
		}
		result = append(result, oci.ExtraFile{
			Source:   filename,
			Dest:     path.Join(destination, filepath.ToSlash(relative)),
			Mode:     int64(info.Mode().Perm()),
			Category: category,
		})
		return nil
	})
	return result, err
}

type FreezeInventory struct {
	Version  int          `json:"version"`
	Language string       `json:"language"`
	Config   string       `json:"config"`
	Files    []FrozenFile `json:"files"`
}

type FrozenFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func (loaded Loaded) WriteFreezeInventory() (string, error) {
	files, err := loaded.ImageFiles()
	if err != nil {
		return "", err
	}
	inventory := FreezeInventory{
		Version: 1, Language: loaded.Config.Language, Config: loaded.configReference(),
		Files: make([]FrozenFile, 0, len(files)),
	}
	seen := map[string]bool{}
	for _, file := range files {
		if seen[file.Source] {
			continue
		}
		seen[file.Source] = true
		source, err := os.Open(file.Source)
		if err != nil {
			return "", err
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, source)
		closeErr := source.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		relative, err := filepath.Rel(loaded.Root, file.Source)
		if err != nil || strings.HasPrefix(relative, "..") {
			relative = file.Source
		}
		inventory.Files = append(inventory.Files, FrozenFile{
			Path: filepath.ToSlash(relative), Size: size,
			SHA256: hex.EncodeToString(hash.Sum(nil)),
		})
	}
	sort.Slice(inventory.Files, func(i, j int) bool { return inventory.Files[i].Path < inventory.Files[j].Path })
	data, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return "", err
	}
	directory := filepath.Join(loaded.Root, ".platform-factory")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", err
	}
	filename := filepath.Join(directory, "freeze.lock.json")
	data = append(data, '\n')
	if err := os.WriteFile(filename, data, 0o644); err != nil {
		return "", err
	}
	return filename, nil
}

func (loaded Loaded) configReference() string {
	relative, err := filepath.Rel(loaded.Root, loaded.File)
	if err == nil && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(relative)
	}
	return filepath.Base(loaded.File)
}
