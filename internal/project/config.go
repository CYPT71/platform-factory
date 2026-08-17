// Package project loads and validates declarative, language-neutral project
// image configuration.
package project

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ConfigNames is ordered by discovery precedence.
var ConfigNames = []string{
	"pf.yaml", "pf.yml",
	".config_image.yaml", ".config_image.yml",
	".config_img.yaml", ".config_img.yml",
	".config_image.json", ".config_img.json",
	"platform-factory.yaml", "platform-factory.yml",
}

type DependencyManagement struct {
	Mode string `yaml:"mode" json:"mode"`
	File string `yaml:"file,omitempty" json:"file,omitempty"`
}

type Config struct {
	Version              int                   `yaml:"version" json:"version"`
	Language             string                `yaml:"language" json:"language"`
	DependencyManagement *DependencyManagement `yaml:"dependency_management,omitempty" json:"dependency_management,omitempty"`
	Project              string                `yaml:"project,omitempty" json:"project,omitempty"`
	Runtime              string                `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Artifact             string                `yaml:"artifact" json:"artifact"`
	Output               string                `yaml:"output,omitempty" json:"output,omitempty"`
	Image                string                `yaml:"image,omitempty" json:"image,omitempty"`
	Tag                  string                `yaml:"tag,omitempty" json:"tag,omitempty"`
	Platform             string                `yaml:"platform,omitempty" json:"platform,omitempty"`
	Entrypoint           string                `yaml:"entrypoint,omitempty" json:"entrypoint,omitempty"`
	Profile              string                `yaml:"profile,omitempty" json:"profile,omitempty"`
	Args                 []string              `yaml:"args,omitempty" json:"args,omitempty"`
	Env                  map[string]string     `yaml:"env,omitempty" json:"env,omitempty"`
	User                 string                `yaml:"user,omitempty" json:"user,omitempty"`
	Isolation            string                `yaml:"isolation,omitempty" json:"isolation,omitempty"`
	RuntimeEngine        string                `yaml:"runtime_engine,omitempty" json:"runtime_engine,omitempty"`
	Network              string                `yaml:"network,omitempty" json:"network,omitempty"`
	Ports                []string              `yaml:"ports,omitempty" json:"ports,omitempty"`
	IncludeProject       *bool                 `yaml:"include_project,omitempty" json:"include_project,omitempty"`
	Include              []Dependency          `yaml:"include,omitempty" json:"include,omitempty"`
	SharedDeps           []Dependency          `yaml:"shared_deps,omitempty" json:"shared_deps,omitempty"`
	FreezeCommand        []string              `yaml:"freeze_command,omitempty" json:"freeze_command,omitempty"`
	BuildCommand         []string              `yaml:"build_command,omitempty" json:"build_command,omitempty"`
	SemanticLayers       bool                  `yaml:"semantic_layers,omitempty" json:"semantic_layers,omitempty"`
	LegacyDisks          *LegacyDiskConfig     `yaml:"legacy_disks,omitempty" json:"legacy_disks,omitempty"`
	// LanguagePlugin delegates freeze and dependency-layer creation.
	LanguagePlugin bool `yaml:"language_plugin,omitempty" json:"language_plugin,omitempty"`
}

// LegacyDiskConfig records relative legacy VM disk paths discovered by init.
type LegacyDiskConfig struct {
	Boot string   `yaml:"boot" json:"boot"`
	Data []string `yaml:"data,omitempty" json:"data,omitempty"`
}

type Dependency struct {
	Source      string `yaml:"source" json:"source"`
	Destination string `yaml:"destination" json:"destination"`
	// Category selects a semantic OCI layer.
	Category string `yaml:"category,omitempty" json:"category,omitempty"`
}

type Loaded struct {
	Config Config `json:"config"`
	File   string `json:"file"`
	Root   string `json:"root"`
}

func Discover(start, explicit string) (Loaded, error) {
	var loaded Loaded
	var err error
	if explicit != "" {
		loaded, err = Load(explicit)
	} else {
		var filename string
		filename, err = FindConfigFile(start)
		if err == nil {
			loaded, err = Load(filename)
		}
	}
	if err != nil {
		return Loaded{}, err
	}
	lockName := "pf.lock"
	base := filepath.Base(loaded.File)
	if base == "platform-factory.yaml" || base == "platform-factory.yml" {
		lockName = "platform-factory.lock"
	}
	lockPath := filepath.Join(filepath.Dir(loaded.File), lockName)
	otherLockName := "platform-factory.lock"
	if lockName == otherLockName {
		otherLockName = "pf.lock"
	}
	otherLockPath := filepath.Join(filepath.Dir(loaded.File), otherLockName)
	if _, otherErr := os.Lstat(otherLockPath); otherErr == nil {
		return Loaded{}, fmt.Errorf("ambiguous project locks: both %s and %s exist; keep only the lock matching %s", lockName, otherLockName, base)
	} else if !errors.Is(otherErr, os.ErrNotExist) {
		return Loaded{}, fmt.Errorf("inspect alternate project lock: %w", otherErr)
	}
	if info, statErr := os.Lstat(lockPath); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return Loaded{}, fmt.Errorf("project lock %s must be a regular file, not a symlink", lockPath)
		}
		if _, err := LoadLock(lockPath); err != nil {
			return Loaded{}, err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Loaded{}, fmt.Errorf("inspect project lock: %w", statErr)
	}
	return loaded, nil
}

// FindConfigFile walks from start upward to the filesystem root and
// returns the nearest supported project config file, without loading it.
func FindConfigFile(start string) (string, error) {
	root, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		var matches []string
		for _, name := range ConfigNames {
			candidate := filepath.Join(root, name)
			if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
				matches = append(matches, candidate)
			}
		}
		if len(matches) > 1 {
			return "", fmt.Errorf("multiple project configurations found in %s; keep exactly one: %s", root, strings.Join(matches, ", "))
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		parent := filepath.Dir(root)
		if parent == root {
			break
		}
		root = parent
	}
	return "", fmt.Errorf("no project image config found (supported: %s)", strings.Join(ConfigNames, ", "))
}

func Load(filename string) (Loaded, error) {
	file, err := os.Open(filename)
	if err != nil {
		return Loaded{}, fmt.Errorf("open project config: %w", err)
	}
	defer file.Close()
	decoder := yaml.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.KnownFields(true)
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Loaded{}, fmt.Errorf("decode project config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Loaded{}, errors.New("project config must contain exactly one YAML/JSON document")
	}
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return Loaded{}, err
	}
	root := filepath.Dir(absolute)
	if config.Project != "" {
		if filepath.IsAbs(config.Project) {
			root = filepath.Clean(config.Project)
		} else {
			root = filepath.Clean(filepath.Join(root, config.Project))
		}
	}
	loaded := Loaded{Config: config, File: absolute, Root: root}
	loaded.defaults()
	if err := loaded.Validate(); err != nil {
		return Loaded{}, err
	}
	return loaded, nil
}

func (loaded *Loaded) defaults() {
	config := &loaded.Config
	if config.Version == 0 {
		config.Version = 1
	}
	if config.Output == "" {
		config.Output = ".platform-factory/image"
	}
	if config.Image == "" {
		config.Image = filepath.Base(loaded.Root)
	}
	if config.Tag == "" {
		config.Tag = "latest"
	}
	if config.Platform == "" {
		config.Platform = "linux/amd64"
	}
	if config.Isolation == "" {
		config.Isolation = "container"
	}
	if config.RuntimeEngine == "" {
		config.RuntimeEngine = "docker"
	}
	if config.Network == "" {
		config.Network = "none"
	}
}

func (loaded Loaded) Validate() error {
	config := loaded.Config
	if dependency := config.DependencyManagement; dependency != nil {
		switch dependency.Mode {
		case "none", "manifest", "unresolved", "external", "unknown":
		default:
			return errors.New("dependency_management.mode must be none, manifest, unresolved, external, or unknown")
		}
		if dependency.Mode == "manifest" && strings.TrimSpace(dependency.File) == "" {
			return errors.New("dependency_management.file is required in manifest mode")
		}
		if dependency.Mode != "manifest" && dependency.File != "" {
			return errors.New("dependency_management.file is only valid in manifest mode")
		}
	}
	if config.Version > CurrentConfigVersion {
		return fmt.Errorf("config version %d is newer than this platform-factory supports (max %d); upgrade platform-factory",
			config.Version, CurrentConfigVersion)
	}
	if config.Version != CurrentConfigVersion {
		return fmt.Errorf("unsupported project config version %d (run \"platform-factory project migrate\")", config.Version)
	}
	// A legacy-VM-disk project has no language or build artifact at all
	// by definition - the disk itself is the deliverable. Requiring
	// these fields would force `pf init` to invent a fake value just to
	// satisfy the schema, which is exactly the placeholder-junk problem
	// this exemption avoids; see docs/legacy-vm-disk-boot.md. The
	// exemption applies whenever legacy_disks is present at all - `pf
	// init` only ever also writes language/artifact alongside it when
	// real application source was separately, confidently found, but
	// that is not a separate requirement Validate enforces here.
	if config.LegacyDisks == nil {
		if config.Language == "" {
			return errors.New(`platform-factory.yaml is missing "language" - add a line like language: go ` +
				`(or run "pf init" again, which asks for it)`)
		}
		if config.Artifact == "" && config.Runtime == "" {
			return errors.New(`platform-factory.yaml is missing "artifact" - add a line like artifact: path/to/your/built/executable, ` +
				`relative to this file`)
		}
	}
	if config.Isolation != "container" && config.Isolation != "microvm" {
		return errors.New("isolation must be container or microvm")
	}
	if config.RuntimeEngine != "docker" && config.RuntimeEngine != "podman" {
		return errors.New("runtime_engine must be docker or podman")
	}
	if config.Platform != "linux/amd64" && config.Platform != "linux/arm64" {
		return errors.New("platform must be linux/amd64 or linux/arm64")
	}
	for _, command := range [][]string{config.FreezeCommand, config.BuildCommand} {
		for _, value := range command {
			if value == "" || strings.ContainsRune(value, 0) {
				return errors.New("commands must contain non-empty, NUL-free arguments")
			}
		}
	}
	for _, dependency := range append(append([]Dependency(nil), config.Include...), config.SharedDeps...) {
		if dependency.Source == "" || dependency.Destination == "" ||
			strings.ContainsRune(dependency.Source, 0) ||
			!strings.HasPrefix(dependency.Destination, "/") ||
			path.Clean(dependency.Destination) != dependency.Destination ||
			dependency.Destination == "/" {
			return errors.New("include/shared_deps require a NUL-free source and clean absolute non-root destination")
		}
		switch dependency.Category {
		case "", CategoryToolchain, CategoryDependencies, CategoryApplication, CategoryMetadata:
		default:
			return fmt.Errorf("unknown dependency category %q (want toolchain, dependencies, application, metadata, or empty)", dependency.Category)
		}
	}
	return nil
}

func (loaded Loaded) Resolve(value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(loaded.Root, filepath.Clean(value))
}

func (loaded Loaded) Output() string { return loaded.Resolve(loaded.Config.Output) }
