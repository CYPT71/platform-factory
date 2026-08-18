package oci

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// BuildConfig is the strict declarative runtime contract accepted by
// oci-builder -config. Unknown JSON fields are rejected to prevent silent
// configuration drift.
type BuildConfig struct {
	Entrypoint    string            `json:"entrypoint"`
	Profile       string            `json:"profile,omitempty"`
	Args          []string          `json:"args,omitempty"`
	WorkingDir    string            `json:"working_dir,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	User          string            `json:"user,omitempty"`
	Home          string            `json:"home,omitempty"`
	IdentityFiles bool              `json:"identity_files,omitempty"`
	SystemFiles   SystemFiles       `json:"system_files,omitempty"`
	Ports         []string          `json:"ports,omitempty"`
	Volumes       []string          `json:"volumes,omitempty"`
	WritablePaths []string          `json:"writable_paths,omitempty"`
	Healthcheck   *Healthcheck      `json:"healthcheck,omitempty"`
	// SemanticLayers requests one layer per semantic category (toolchain,
	// dependencies, application, metadata) instead of the default single
	// layer. It changes layer digests, never filesystem content.
	SemanticLayers bool `json:"semantic_layers,omitempty"`
}

type SystemFiles struct {
	CACertificates string `json:"ca_certificates,omitempty"`
	Timezone       string `json:"timezone,omitempty"`
	LocaleArchive  string `json:"locale_archive,omitempty"`
}

type Healthcheck struct {
	Command  []string `json:"command"`
	Interval string   `json:"interval,omitempty"`
	Timeout  string   `json:"timeout,omitempty"`
	Retries  int      `json:"retries,omitempty"`
}

func LoadBuildConfig(filename string) (BuildConfig, error) {
	file, err := os.Open(filename)
	if err != nil {
		return BuildConfig{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var config BuildConfig
	if err := decoder.Decode(&config); err != nil {
		return BuildConfig{}, fmt.Errorf("decode config: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return BuildConfig{}, errors.New("config must contain exactly one JSON object")
	}
	if err := config.Validate(); err != nil {
		return BuildConfig{}, err
	}
	return config, nil
}

func (c BuildConfig) Validate() error {
	if err := validateContainerPath("entrypoint", c.Entrypoint, false); err != nil {
		return err
	}
	if c.WorkingDir != "" {
		if err := validateContainerPath("working_dir", c.WorkingDir, false); err != nil {
			return err
		}
	}
	supportedProfiles := map[string]bool{
		"": true, "static": true, "glibc": true, "musl": true,
		"python": true, "node": true, "java": true, "dotnet": true,
		"ruby": true, "php": true,
	}
	if !supportedProfiles[c.Profile] {
		return fmt.Errorf("unsupported profile %q (supported: static, glibc, musl, python, node, java, dotnet, ruby, php)", c.Profile)
	}
	interpreters := map[string]map[string]bool{
		"python": {"python": true, "python3": true},
		"node":   {"node": true},
		"java":   {"java": true},
		"dotnet": {"dotnet": true},
		"ruby":   {"ruby": true},
		"php":    {"php": true},
	}
	if allowed, interpreted := interpreters[c.Profile]; interpreted {
		if !allowed[path.Base(c.Entrypoint)] {
			return fmt.Errorf("%s profile requires a matching runtime entrypoint", c.Profile)
		}
		if len(c.Args) == 0 {
			return fmt.Errorf("%s profile requires an explicit application argument", c.Profile)
		}
	}
	if _, _, err := parseRuntimeUser(c.User); err != nil {
		return err
	}
	if c.Home != "" {
		if err := validateContainerPath("home", c.Home, false); err != nil {
			return err
		}
	}
	for name, source := range map[string]string{
		"ca_certificates": c.SystemFiles.CACertificates,
		"timezone":        c.SystemFiles.Timezone,
		"locale_archive":  c.SystemFiles.LocaleArchive,
	} {
		if strings.ContainsRune(source, 0) {
			return fmt.Errorf("system_files.%s contains NUL", name)
		}
	}
	for _, list := range []struct {
		name   string
		values []string
	}{
		{"volume", c.Volumes},
		{"writable_path", c.WritablePaths},
	} {
		seen := map[string]bool{}
		for _, value := range list.values {
			if err := validateContainerPath(list.name, value, false); err != nil {
				return err
			}
			if seen[value] {
				return fmt.Errorf("duplicate %s %q", list.name, value)
			}
			seen[value] = true
		}
	}
	for key := range c.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("invalid environment variable name %q", key)
		}
	}
	for _, port := range c.Ports {
		number, protocol, ok := strings.Cut(port, "/")
		value, err := strconv.Atoi(number)
		if !ok || err != nil || value < 1 || value > 65535 || (protocol != "tcp" && protocol != "udp") {
			return fmt.Errorf("invalid port %q (use for example 8080/tcp)", port)
		}
	}
	if c.Healthcheck != nil && len(c.Healthcheck.Command) == 0 {
		return errors.New("healthcheck.command must not be empty")
	}
	if c.Healthcheck != nil {
		for name, value := range map[string]string{"interval": c.Healthcheck.Interval, "timeout": c.Healthcheck.Timeout} {
			if value != "" {
				duration, err := time.ParseDuration(value)
				if err != nil || duration <= 0 {
					return fmt.Errorf("healthcheck.%s must be a positive Go duration", name)
				}
			}
		}
		if c.Healthcheck.Retries < 0 {
			return errors.New("healthcheck.retries must not be negative")
		}
	}
	return nil
}

func parseRuntimeUser(value string) (int, int, error) {
	if value == "" {
		return 65532, 65532, nil
	}
	uidText, gidText, found := strings.Cut(value, ":")
	if !found {
		gidText = uidText
	}
	uid, uidErr := strconv.Atoi(uidText)
	gid, gidErr := strconv.Atoi(gidText)
	if uidErr != nil || gidErr != nil || uid <= 0 || gid <= 0 {
		return 0, 0, errors.New("user must be a positive numeric UID or UID:GID")
	}
	return uid, gid, nil
}

func validateContainerPath(field, value string, allowEmpty bool) error {
	if allowEmpty && value == "" {
		return nil
	}
	if value == "" || !strings.HasPrefix(value, "/") || path.Clean(value) != value || value == "/" {
		return fmt.Errorf("%s must be an absolute, clean container path", field)
	}
	return nil
}

func sortedEnv(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}
