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
)

// ExtraFile mirrors internal/oci.ExtraFile field-for-field so a caller (the
// composition root in cmd/, which is allowed to depend on both) can convert
// one into the other with a plain literal. internal/project does not import
// internal/oci itself - domain packages must not depend on concrete
// infrastructure packages, enforced by internal/archtest.
type ExtraFile struct {
	Dest, Source string
	Mode         int64
	Category     string
}

// Semantic layer categories. Matches internal/oci.Category* by value (both
// are plain strings, and internal/oci.Build validates an ExtraFile.Category
// against exactly these four names) rather than by importing that package.
const (
	CategoryToolchain    = "toolchain"
	CategoryDependencies = "dependencies"
	CategoryApplication  = "application"
	CategoryMetadata     = "metadata"
)

func (loaded Loaded) ImageFiles() ([]ExtraFile, error) {
	config := loaded.Config
	includeProject := config.Language != "go" && config.Language != "compiled" && config.Language != "custom"
	if config.IncludeProject != nil {
		includeProject = *config.IncludeProject
	}
	dependencies := append([]Dependency(nil), config.Include...)
	if includeProject {
		dependencies = append([]Dependency{{Source: ".", Destination: "/app", Category: CategoryApplication}}, dependencies...)
	}
	for index := range config.SharedDeps {
		shared := config.SharedDeps[index]
		if shared.Category == "" {
			shared.Category = CategoryDependencies
		}
		dependencies = append(dependencies, shared)
	}
	for _, name := range []string{"shared_deps", "Shared_deps", "shared-deps"} {
		candidate := filepath.Join(loaded.Root, name)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() && !hasSource(dependencies, candidate, loaded) {
			dependencies = append(dependencies, Dependency{
				Source: candidate, Destination: "/app/shared_deps",
				Category: CategoryDependencies,
			})
			break
		}
	}

	output := loaded.Output()
	var files []ExtraFile
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

// generatedPlatformFactoryDirs are .platform-factory/<name> subdirectories
// this tool itself writes as build/publish/deploy evidence - never a real
// project input. Sweeping them into ImageFiles() (and therefore into
// WriteFreezeInventory/VerifyFreezeInventory's hashed input set) meant every
// build invalidated its own freeze: build writes .platform-factory/release/
// provenance.json, the next build's frozen-input check sees that file
// changed and refuses to proceed until `pf freeze` runs again, which the
// very next build then invalidates the same way. .platform-factory/deps -
// the resolved runtime pf freeze itself pins - is deliberately not in this
// list; it is a real frozen input, not build output.
var generatedPlatformFactoryDirs = map[string]bool{
	".platform-factory/release":     true,
	".platform-factory/publication": true,
	".platform-factory/deployment":  true,
}

func collectTree(source, destination, output, category string) ([]ExtraFile, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("symbolic links are not accepted")
	}
	if info.Mode().IsRegular() {
		return []ExtraFile{{Source: source, Dest: destination, Mode: int64(info.Mode().Perm()), Category: category}}, nil
	}
	if !info.IsDir() {
		return nil, errors.New("source must be a regular file or directory")
	}
	var result []ExtraFile
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
		relativeSlash := filepath.ToSlash(relative)
		if relativeSlash == ".platform-factory/freeze.lock.json" {
			return nil
		}
		if entry.IsDir() && generatedPlatformFactoryDirs[relativeSlash] {
			return filepath.SkipDir
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
		result = append(result, ExtraFile{
			Source:   filename,
			Dest:     path.Join(destination, relativeSlash),
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

// VerifyFreezeInventory strictly reloads the last freeze result and verifies
// every recorded file against its size and SHA-256 before a build can consume
// it. Paths must remain relative to the project root and may not escape it.
func (loaded Loaded) VerifyFreezeInventory() error {
	filename := filepath.Join(loaded.Root, ".platform-factory", "freeze.lock.json")
	file, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("open freeze inventory: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16<<20))
	decoder.DisallowUnknownFields()
	var inventory FreezeInventory
	if err := decoder.Decode(&inventory); err != nil {
		return fmt.Errorf("decode freeze inventory: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("freeze inventory must contain exactly one JSON value")
	}
	if inventory.Version != 1 || inventory.Language != loaded.Config.Language || inventory.Config != loaded.configReference() {
		return errors.New("freeze inventory does not match the loaded project configuration")
	}
	imageFiles, err := loaded.ImageFiles()
	if err != nil {
		return fmt.Errorf("resolve frozen inputs: %w", err)
	}
	expected := make(map[string]string, len(imageFiles))
	for _, imageFile := range imageFiles {
		relative, relativeErr := filepath.Rel(loaded.Root, imageFile.Source)
		key := filepath.ToSlash(relative)
		if relativeErr != nil || strings.HasPrefix(relative, "..") {
			key = filepath.ToSlash(imageFile.Source)
		}
		expected[key] = imageFile.Source
	}
	previous := ""
	for _, frozen := range inventory.Files {
		path, declared := expected[frozen.Path]
		if frozen.Path == "" || !declared || frozen.Path <= previous {
			return fmt.Errorf("freeze inventory contains unsafe or unsorted path %q", frozen.Path)
		}
		previous = frozen.Path
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("frozen file %s is missing or unsafe", frozen.Path)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if size != frozen.Size || hex.EncodeToString(hash.Sum(nil)) != frozen.SHA256 {
			return fmt.Errorf("frozen file %s changed after `pf freeze`; run `pf freeze` again", frozen.Path)
		}
	}
	return nil
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
