package layout

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

// DiffReport explains every divergence between two verified OCI layouts.
// Equal is true only when every reference/platform pair resolves to the
// same manifest digest in both layouts.
type DiffReport struct {
	Equal     bool           `json:"equal"`
	A         string         `json:"a"`
	B         string         `json:"b"`
	Platforms []PlatformDiff `json:"platforms,omitempty"`
	Notes     []string       `json:"notes,omitempty"`
}

// PlatformDiff compares one reference/platform pair present in both
// layouts. When the manifest digests match, the images are identical and
// no deeper comparison is reported.
type PlatformDiff struct {
	Reference string      `json:"reference,omitempty"`
	Platform  string      `json:"platform"`
	DigestA   string      `json:"digest_a"`
	DigestB   string      `json:"digest_b"`
	Equal     bool        `json:"equal"`
	Config    []FieldDiff `json:"config,omitempty"`
	Layers    []LayerDiff `json:"layers,omitempty"`
	Notes     []string    `json:"notes,omitempty"`
}

// FieldDiff reports one image-config field whose values differ.
type FieldDiff struct {
	Field string `json:"field"`
	A     string `json:"a"`
	B     string `json:"b"`
}

// LayerDiff explains one divergent layer pair, including the first
// filesystem entry where the two tar streams part ways.
type LayerDiff struct {
	Index           int        `json:"index"`
	DigestA         string     `json:"digest_a,omitempty"`
	DigestB         string     `json:"digest_b,omitempty"`
	FirstDivergence *EntryDiff `json:"first_divergence,omitempty"`
	Added           int        `json:"added"`
	Removed         int        `json:"removed"`
	Changed         int        `json:"changed"`
}

// EntryDiff names a filesystem entry and how it differs.
type EntryDiff struct {
	Path   string `json:"path"`
	Detail string `json:"detail"`
}

type diffImageConfig struct {
	Created      string `json:"created"`
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Config       struct {
		User         string              `json:"User"`
		Entrypoint   []string            `json:"Entrypoint"`
		Cmd          []string            `json:"Cmd"`
		WorkingDir   string              `json:"WorkingDir"`
		Env          []string            `json:"Env"`
		ExposedPorts map[string]struct{} `json:"ExposedPorts"`
		Volumes      map[string]struct{} `json:"Volumes"`
		Labels       map[string]string   `json:"Labels"`
	} `json:"config"`
}

// Diff verifies both layouts, then explains every divergence between
// them. Both inputs are untrusted: a layout that fails verification
// fails the diff.
func Diff(rootA, rootB string) (DiffReport, error) {
	if _, err := Verify(rootA); err != nil {
		return DiffReport{}, fmt.Errorf("layout A: %w", err)
	}
	if _, err := Verify(rootB); err != nil {
		return DiffReport{}, fmt.Errorf("layout B: %w", err)
	}
	manifestsA, err := manifestsByTarget(rootA)
	if err != nil {
		return DiffReport{}, fmt.Errorf("layout A: %w", err)
	}
	manifestsB, err := manifestsByTarget(rootB)
	if err != nil {
		return DiffReport{}, fmt.Errorf("layout B: %w", err)
	}
	report := DiffReport{Equal: true, A: rootA, B: rootB}
	for _, key := range sortedKeys(manifestsA) {
		if _, ok := manifestsB[key]; !ok {
			report.Equal = false
			report.Notes = append(report.Notes, fmt.Sprintf("%s only in layout A", describeTarget(key)))
		}
	}
	for _, key := range sortedKeys(manifestsB) {
		if _, ok := manifestsA[key]; !ok {
			report.Equal = false
			report.Notes = append(report.Notes, fmt.Sprintf("%s only in layout B", describeTarget(key)))
		}
	}
	for _, key := range sortedKeys(manifestsA) {
		descriptorB, ok := manifestsB[key]
		if !ok {
			continue
		}
		descriptorA := manifestsA[key]
		platform := PlatformDiff{
			Reference: descriptorA.Annotations["org.opencontainers.image.ref.name"],
			Platform:  descriptorA.Platform.OS + "/" + descriptorA.Platform.Architecture,
			DigestA:   descriptorA.Digest, DigestB: descriptorB.Digest,
			Equal: descriptorA.Digest == descriptorB.Digest,
		}
		if !platform.Equal {
			report.Equal = false
			if err := explainManifestDiff(rootA, rootB, descriptorA, descriptorB, &platform); err != nil {
				return DiffReport{}, err
			}
		}
		report.Platforms = append(report.Platforms, platform)
	}
	return report, nil
}

func manifestsByTarget(root string) (map[string]descriptor, error) {
	var idx index
	if err := decodeFile(filepath.Join(root, "index.json"), &idx); err != nil {
		return nil, fmt.Errorf("index: %w", err)
	}
	result := make(map[string]descriptor, len(idx.Manifests))
	for _, manifestDescriptor := range idx.Manifests {
		key := manifestDescriptor.Annotations["org.opencontainers.image.ref.name"] + "\x00" +
			manifestDescriptor.Platform.OS + "/" + manifestDescriptor.Platform.Architecture
		result[key] = manifestDescriptor
	}
	return result, nil
}

func sortedKeys(values map[string]descriptor) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func describeTarget(key string) string {
	reference, platform, _ := strings.Cut(key, "\x00")
	if reference == "" {
		return platform
	}
	return reference + " " + platform
}

func explainManifestDiff(rootA, rootB string, descriptorA, descriptorB descriptor, platform *PlatformDiff) error {
	manifestA, configA, err := readManifestAndConfig(rootA, descriptorA)
	if err != nil {
		return fmt.Errorf("layout A: %w", err)
	}
	manifestB, configB, err := readManifestAndConfig(rootB, descriptorB)
	if err != nil {
		return fmt.Errorf("layout B: %w", err)
	}
	platform.Config = diffConfigs(configA, configB)
	if len(manifestA.Layers) != len(manifestB.Layers) {
		platform.Notes = append(platform.Notes,
			fmt.Sprintf("layer count differs: %d vs %d", len(manifestA.Layers), len(manifestB.Layers)))
	}
	for index := 0; index < len(manifestA.Layers) || index < len(manifestB.Layers); index++ {
		layer := LayerDiff{Index: index}
		switch {
		case index >= len(manifestB.Layers):
			layer.DigestA = manifestA.Layers[index].Digest
			layer.FirstDivergence = &EntryDiff{Path: "", Detail: "layer only in A"}
		case index >= len(manifestA.Layers):
			layer.DigestB = manifestB.Layers[index].Digest
			layer.FirstDivergence = &EntryDiff{Path: "", Detail: "layer only in B"}
		default:
			layer.DigestA = manifestA.Layers[index].Digest
			layer.DigestB = manifestB.Layers[index].Digest
			if layer.DigestA == layer.DigestB {
				continue
			}
			if err := explainLayerDiff(rootA, rootB, manifestA.Layers[index], manifestB.Layers[index], &layer); err != nil {
				return err
			}
		}
		platform.Layers = append(platform.Layers, layer)
	}
	return nil
}

func readManifestAndConfig(root string, manifestDescriptor descriptor) (manifest, diffImageConfig, error) {
	expected := map[string]bool{}
	manifestData, err := readDescriptor(root, manifestDescriptor, expected)
	if err != nil {
		return manifest{}, diffImageConfig{}, err
	}
	var document manifest
	if err := json.Unmarshal(manifestData, &document); err != nil {
		return manifest{}, diffImageConfig{}, errors.New("invalid manifest")
	}
	configData, err := readDescriptor(root, document.Config, expected)
	if err != nil {
		return manifest{}, diffImageConfig{}, err
	}
	var config diffImageConfig
	if err := json.Unmarshal(configData, &config); err != nil {
		return manifest{}, diffImageConfig{}, errors.New("invalid image config")
	}
	return document, config, nil
}

func diffConfigs(a, b diffImageConfig) []FieldDiff {
	var result []FieldDiff
	compare := func(field, valueA, valueB string) {
		if valueA != valueB {
			result = append(result, FieldDiff{Field: field, A: valueA, B: valueB})
		}
	}
	compare("created", a.Created, b.Created)
	compare("architecture", a.Architecture, b.Architecture)
	compare("os", a.OS, b.OS)
	compare("user", a.Config.User, b.Config.User)
	compare("entrypoint", strings.Join(a.Config.Entrypoint, " "), strings.Join(b.Config.Entrypoint, " "))
	compare("cmd", strings.Join(a.Config.Cmd, " "), strings.Join(b.Config.Cmd, " "))
	compare("working_dir", a.Config.WorkingDir, b.Config.WorkingDir)
	compare("env", strings.Join(a.Config.Env, "\n"), strings.Join(b.Config.Env, "\n"))
	compare("exposed_ports", joinSet(a.Config.ExposedPorts), joinSet(b.Config.ExposedPorts))
	compare("volumes", joinSet(a.Config.Volumes), joinSet(b.Config.Volumes))
	compare("labels", joinMap(a.Config.Labels), joinMap(b.Config.Labels))
	return result
}

func joinSet(values map[string]struct{}) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func joinMap(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+values[key])
	}
	return strings.Join(pairs, ",")
}

type layerEntry struct {
	typeflag byte
	mode     int64
	size     int64
	digest   string
}

func explainLayerDiff(rootA, rootB string, layerA, layerB descriptor, layer *LayerDiff) error {
	entriesA, err := readLayerEntries(rootA, layerA)
	if err != nil {
		return fmt.Errorf("layout A: %w", err)
	}
	entriesB, err := readLayerEntries(rootB, layerB)
	if err != nil {
		return fmt.Errorf("layout B: %w", err)
	}
	paths := map[string]bool{}
	for name := range entriesA {
		paths[name] = true
	}
	for name := range entriesB {
		paths[name] = true
	}
	sorted := make([]string, 0, len(paths))
	for name := range paths {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	for _, name := range sorted {
		entryA, inA := entriesA[name]
		entryB, inB := entriesB[name]
		var detail string
		switch {
		case !inB:
			layer.Removed++
			detail = "only in A"
		case !inA:
			layer.Added++
			detail = "only in B"
		case entryA != entryB:
			layer.Changed++
			detail = describeEntryChange(entryA, entryB)
		default:
			continue
		}
		if layer.FirstDivergence == nil {
			layer.FirstDivergence = &EntryDiff{Path: name, Detail: detail}
		}
	}
	if layer.FirstDivergence == nil {
		// Same filesystem entries, different compressed bytes: the
		// layers differ only in their compression settings.
		layer.FirstDivergence = &EntryDiff{Path: "", Detail: "identical entries; compressed bytes differ (compression settings)"}
	}
	return nil
}

func describeEntryChange(a, b layerEntry) string {
	var details []string
	if a.typeflag != b.typeflag {
		details = append(details, fmt.Sprintf("type %q vs %q", a.typeflag, b.typeflag))
	}
	if a.mode != b.mode {
		details = append(details, fmt.Sprintf("mode %o vs %o", a.mode, b.mode))
	}
	if a.size != b.size {
		details = append(details, fmt.Sprintf("size %d vs %d", a.size, b.size))
	}
	if a.digest != b.digest {
		details = append(details, fmt.Sprintf("content sha256 %s vs %s", a.digest, b.digest))
	}
	return strings.Join(details, ", ")
}

func readLayerEntries(root string, layerDescriptor descriptor) (map[string]layerEntry, error) {
	data, err := readDescriptor(root, layerDescriptor, map[string]bool{})
	if err != nil {
		return nil, err
	}
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("invalid gzip layer")
	}
	defer reader.Close()
	archive := tar.NewReader(reader)
	entries := map[string]layerEntry{}
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("invalid tar layer")
		}
		entry := layerEntry{typeflag: header.Typeflag, mode: header.Mode, size: header.Size}
		if header.Typeflag == tar.TypeReg {
			hash := sha256.New()
			if _, err := io.Copy(hash, archive); err != nil {
				return nil, errors.New("invalid tar layer")
			}
			entry.digest = hex.EncodeToString(hash.Sum(nil))
		}
		entries[strings.TrimSuffix(header.Name, "/")] = entry
	}
	return entries, nil
}

// WriteText renders the diff report as stable, line-oriented text.
func (report DiffReport) WriteText(output io.Writer) {
	state := "identical"
	if !report.Equal {
		state = "divergent"
	}
	fmt.Fprintf(output, "%s: %s vs %s\n", state, report.A, report.B)
	for _, note := range report.Notes {
		fmt.Fprintf(output, "  note: %s\n", note)
	}
	for _, platform := range report.Platforms {
		if platform.Equal {
			fmt.Fprintf(output, "  %s %s: identical (%s)\n", platform.Reference, platform.Platform, platform.DigestA)
			continue
		}
		fmt.Fprintf(output, "  %s %s: %s vs %s\n", platform.Reference, platform.Platform, platform.DigestA, platform.DigestB)
		for _, note := range platform.Notes {
			fmt.Fprintf(output, "    note: %s\n", note)
		}
		for _, field := range platform.Config {
			fmt.Fprintf(output, "    config %s: %q vs %q\n", field.Field, field.A, field.B)
		}
		for _, layer := range platform.Layers {
			fmt.Fprintf(output, "    layer %d: +%d -%d ~%d", layer.Index, layer.Added, layer.Removed, layer.Changed)
			if layer.FirstDivergence != nil {
				if layer.FirstDivergence.Path != "" {
					fmt.Fprintf(output, " first divergence %s (%s)", layer.FirstDivergence.Path, layer.FirstDivergence.Detail)
				} else {
					fmt.Fprintf(output, " %s", layer.FirstDivergence.Detail)
				}
			}
			fmt.Fprintln(output)
		}
	}
}
