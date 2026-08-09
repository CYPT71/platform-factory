package sbom

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func inspectPackageMetadata(source, filename string) ([]Package, error) {
	base := strings.ToLower(filepath.Base(filename))
	switch base {
	case "requirements.txt", "requirements.lock":
		return parseRequirements(source, filename)
	case "go.sum":
		return parseFields(source, filename, "go", 0, 1)
	case "cargo.lock":
		return parseKeyValueBlocks(source, filename, "cargo", "name", "version")
	case "gemfile.lock":
		return parseGemfile(source, filename)
	case "package-lock.json", "npm-shrinkwrap.json":
		return parseJSONPackages(source, filename, "npm", "packages")
	case "composer.lock":
		return parseJSONPackages(source, filename, "composer", "packages")
	case "packages.lock.json":
		return parseDotnet(source, filename)
	case "status":
		if strings.Contains(filepath.ToSlash(filename), "/dpkg/") {
			return parseDebianStatus(source, filename)
		}
	case "installed":
		if strings.Contains(filepath.ToSlash(filename), "/apk/") {
			return parseAPKInstalled(source, filename)
		}
	}
	return nil, nil
}

func parseRequirements(source, filename string) ([]Package, error) {
	return scanLines(filename, func(line string) (Package, bool) {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		name, version, ok := strings.Cut(line, "==")
		return Package{Name: strings.TrimSpace(name), Version: strings.TrimSpace(version), Ecosystem: "pypi", Source: source}, ok
	})
}

func parseFields(source, filename, ecosystem string, nameIndex, versionIndex int) ([]Package, error) {
	return scanLines(filename, func(line string) (Package, bool) {
		fields := strings.Fields(line)
		if len(fields) <= versionIndex || strings.HasSuffix(fields[versionIndex], "/go.mod") {
			return Package{}, false
		}
		return Package{Name: fields[nameIndex], Version: fields[versionIndex], Ecosystem: ecosystem, Source: source}, true
	})
}

func parseKeyValueBlocks(source, filename, ecosystem, nameKey, versionKey string) ([]Package, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var result []Package
	current := map[string]string{}
	flush := func() {
		if current[nameKey] != "" && current[versionKey] != "" {
			result = append(result, Package{Name: current[nameKey], Version: current[versionKey], Ecosystem: ecosystem, Source: source})
		}
		current = map[string]string{}
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == "[[package]]" {
			flush()
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok {
			current[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
		}
	}
	flush()
	return result, scanner.Err()
}

func parseGemfile(source, filename string) ([]Package, error) {
	return scanLines(filename, func(line string) (Package, bool) {
		if !strings.HasPrefix(line, "    ") {
			return Package{}, false
		}
		line = strings.TrimSpace(line)
		open := strings.IndexByte(line, '(')
		close := strings.IndexByte(line, ')')
		if open <= 0 || close <= open {
			return Package{}, false
		}
		return Package{Name: strings.TrimSpace(line[:open]), Version: line[open+1 : close], Ecosystem: "gem", Source: source}, true
	})
}

func parseJSONPackages(source, filename, ecosystem, field string) ([]Package, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("sbom: parse %s: %w", source, err)
	}
	var result []Package
	var list []struct{ Name, Version string }
	if json.Unmarshal(document[field], &list) == nil {
		for _, item := range list {
			if item.Name != "" && item.Version != "" {
				result = append(result, Package{Name: item.Name, Version: item.Version, Ecosystem: ecosystem, Source: source})
			}
		}
		return result, nil
	}
	var entries map[string]struct{ Name, Version string }
	if err := json.Unmarshal(document[field], &entries); err != nil {
		return nil, nil
	}
	for path, item := range entries {
		name := item.Name
		if name == "" {
			name = strings.TrimPrefix(path, "node_modules/")
		}
		if name != "" && item.Version != "" {
			result = append(result, Package{Name: name, Version: item.Version, Ecosystem: ecosystem, Source: source})
		}
	}
	return result, nil
}

func parseDotnet(source, filename string) ([]Package, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var document struct {
		Dependencies map[string]map[string]struct {
			Resolved string `json:"resolved"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	var result []Package
	for _, framework := range document.Dependencies {
		for name, item := range framework {
			result = append(result, Package{Name: name, Version: item.Resolved, Ecosystem: "nuget", Source: source})
		}
	}
	return result, nil
}

func parseDebianStatus(source, filename string) ([]Package, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var result []Package
	var name, version string
	flush := func() {
		if name != "" && version != "" {
			result = append(result, Package{Name: name, Version: version, Ecosystem: "deb", Source: source})
		}
		name, version = "", ""
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
		} else if value, ok := strings.CutPrefix(line, "Package:"); ok {
			name = strings.TrimSpace(value)
		} else if value, ok := strings.CutPrefix(line, "Version:"); ok {
			version = strings.TrimSpace(value)
		}
	}
	flush()
	return result, scanner.Err()
}

func parseAPKInstalled(source, filename string) ([]Package, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var result []Package
	var name, version string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "P:") {
			name = strings.TrimPrefix(line, "P:")
		} else if strings.HasPrefix(line, "V:") {
			version = strings.TrimPrefix(line, "V:")
		} else if line == "" {
			if name != "" && version != "" {
				result = append(result, Package{Name: name, Version: version, Ecosystem: "apk", Source: source})
			}
			name, version = "", ""
		}
	}
	if name != "" && version != "" {
		result = append(result, Package{Name: name, Version: version, Ecosystem: "apk", Source: source})
	}
	return result, scanner.Err()
}

func scanLines(filename string, parse func(string) (Package, bool)) ([]Package, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var result []Package
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if item, ok := parse(scanner.Text()); ok && item.Name != "" && item.Version != "" {
			result = append(result, item)
		}
	}
	return result, scanner.Err()
}

func canonicalPackages(packages []Package) []Package {
	seen := map[string]bool{}
	result := packages[:0]
	for _, item := range packages {
		key := item.Ecosystem + "\x00" + item.Name + "\x00" + item.Version + "\x00" + item.Source
		if !seen[key] {
			seen[key] = true
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Ecosystem != result[j].Ecosystem {
			return result[i].Ecosystem < result[j].Ecosystem
		}
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		if result[i].Version != result[j].Version {
			return result[i].Version < result[j].Version
		}
		return result[i].Source < result[j].Source
	})
	return result
}
