// Package oci writes small, deterministic OCI image layouts without a daemon.
package oci

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
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/CYPT71/platform-factory/internal/budget"
)

const (
	manifestMediaType = "application/vnd.oci.image.manifest.v1+json"
	configMediaType   = "application/vnd.oci.image.config.v1+json"
	layerMediaType    = "application/vnd.oci.image.layer.v1.tar+gzip"
	// streamCopyBufferSize balances sequential I/O throughput with bounded
	// memory. One buffer is allocated per layer and cycled across every file.
	streamCopyBufferSize = 1 << 20
)

// Options describes the image to create. Binary must name a regular executable
// file. Output must not already exist; this prevents accidentally replacing an
// image layout with attacker-controlled contents.
type Options struct {
	Binary        string
	Output        string
	Architecture  string
	OS            string
	Entrypoint    string
	Profile       string
	ImageName     string
	Tag           string
	Created       time.Time
	Labels        map[string]string
	ExtraFiles    []ExtraFile
	Args          []string
	WorkingDir    string
	Env           map[string]string
	User          string
	Home          string
	IdentityFiles bool
	Ports         []string
	Volumes       []string
	WritablePaths []string
	Healthcheck   *Healthcheck
	// Compression selects deterministic gzip compression: "best" preserves
	// the historical output, while "fast" is intended for very large images.
	Compression string
	// SemanticLayers splits the image into one layer per non-empty
	// ExtraFile.Category (in a fixed toolchain/dependencies/application/
	// metadata order) instead of the default single layer. Off by default:
	// every existing caller that leaves this unset gets byte-for-byte the
	// same single-layer output as before this field existed.
	SemanticLayers bool
	// TraceID correlates build events across the CLI and CI. It is metadata
	// only and is never written into the reproducible OCI layout.
	TraceID string
	// Observer receives structured lifecycle events. Callers must avoid
	// logging sensitive file contents; this package reports paths, sizes,
	// phases, durations, and digests only.
	Observer func(Event)
	// ExtraLayers are paths to pre-built, uncompressed tar files
	// contributed by external plugins (see plugins/lang-* and
	// docs/language-plugin-layers.md) - a Python interpreter and its
	// installed packages, for example. Each becomes its own manifest
	// layer, appended after every ExtraFiles-derived layer, in the given
	// order. See extralayers.go: every entry is independently validated
	// (clean relative paths, no traversal, no symlinks/hardlinks/devices,
	// bounded size) - a plugin's own claims about its content are never
	// trusted, only what Build itself parses and re-hashes.
	ExtraLayers []string
	// Budget, if non-zero, bounds this Build call's own wall-clock time,
	// CPU time, and heap memory - the whole-process resources
	// internal/budget.Tracker measures via RUSAGE_SELF/runtime.MemStats.
	// Build never spawns child processes, so unlike a sandboxed pipeline
	// stage (whose resources are already bounded by internal/executor's
	// per-process rlimits/cgroups and are invisible to this process's own
	// accounting), this budget correctly reflects the actual build work.
	// Checked once per streamed file; the zero value disables enforcement.
	Budget budget.Budget
	// BinaryMode and PreserveBinaryOwnership are opt-in metadata used by
	// legacy filesystem migration. Ordinary builds retain the established
	// normalized 0555/root ownership defaults.
	BinaryMode              int64
	BinaryUID               uint32
	BinaryGID               uint32
	PreserveBinaryOwnership bool
}

// Event is a structured, non-secret observation of an OCI build phase.
type Event struct {
	Time      time.Time      `json:"time"`
	Level     string         `json:"level"`
	Component string         `json:"component"`
	Operation string         `json:"operation"`
	Phase     string         `json:"phase"`
	TraceID   string         `json:"trace_id,omitempty"`
	Message   string         `json:"message"`
	Duration  time.Duration  `json:"-"`
	Fields    map[string]any `json:"fields,omitempty"`
}

// ExtraFile places an additional file in the layer at a fixed container
// path, alongside the entrypoint - how a dynamically-linked binary is
// packaged (its ELF interpreter and shared libraries, e.g. found via
// ldd, each as one ExtraFile). Every extra file is written 0555: the ELF
// interpreter specifically is loaded by the kernel's own execve(), which
// requires the execute bit, unlike an ordinary library dlopen'd via
// userspace mmap().
type ExtraFile struct {
	// Dest is the absolute, clean container path this file is written to.
	Dest string
	// Source is the host path its content is read from at build time.
	Source string
	// Mode defaults to 0555 for executable ELF dependencies. Declarative
	// system data uses 0444.
	Mode int64
	// Category groups this file into a semantic layer when
	// Options.SemanticLayers is set; ignored otherwise. Empty defaults to
	// CategoryApplication.
	Category string
	// PreserveOwnership opts this file into source UID/GID preservation. The
	// zero value deliberately remains normalized root ownership.
	UID               uint32
	GID               uint32
	PreserveOwnership bool
}

// Semantic layer categories for Options.SemanticLayers, applied in this
// fixed order so layer order is deterministic regardless of input order.
const (
	CategoryToolchain    = "toolchain"
	CategoryDependencies = "dependencies"
	CategoryApplication  = "application"
	CategoryMetadata     = "metadata"
)

var semanticLayerOrder = []string{CategoryToolchain, CategoryDependencies, CategoryApplication, CategoryMetadata}

func validCategory(category string) bool {
	for _, known := range semanticLayerOrder {
		if category == known {
			return true
		}
	}
	return false
}

type descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Platform    *platform         `json:"platform,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}
type platform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
}
type rootFS struct {
	Type    string   `json:"type"`
	DiffIDs []string `json:"diff_ids"`
}
type imageConfig struct {
	Created      string `json:"created,omitempty"`
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Config       struct {
		User         string              `json:"User"`
		Entrypoint   []string            `json:"Entrypoint"`
		Cmd          []string            `json:"Cmd,omitempty"`
		WorkingDir   string              `json:"WorkingDir,omitempty"`
		Env          []string            `json:"Env,omitempty"`
		ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
		Volumes      map[string]struct{} `json:"Volumes,omitempty"`
		Healthcheck  *imageHealthcheck   `json:"Healthcheck,omitempty"`
		Labels       map[string]string   `json:"Labels,omitempty"`
	} `json:"config"`
	RootFS rootFS `json:"rootfs"`
}
type imageHealthcheck struct {
	Test     []string `json:"Test"`
	Interval int64    `json:"Interval,omitempty"`
	Timeout  int64    `json:"Timeout,omitempty"`
	Retries  int      `json:"Retries,omitempty"`
}
type manifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	Config        descriptor   `json:"config"`
	Layers        []descriptor `json:"layers"`
}
type index struct {
	SchemaVersion int          `json:"schemaVersion"`
	Manifests     []descriptor `json:"manifests"`
}

// Build writes an OCI Image Layout and returns the digest of its manifest.
func Build(opts Options) (string, error) {
	started := time.Now()
	observe(opts, "debug", "validate", "validating build options and input", nil, 0)
	if err := normalize(&opts); err != nil {
		observe(opts, "error", "validate", "build validation failed", map[string]any{"error": err.Error()}, time.Since(started))
		return "", err
	}
	info, err := os.Stat(opts.Binary)
	if err != nil {
		return "", fmt.Errorf("stat binary: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("binary must be a regular file")
	}
	// Windows filesystems do not expose the Unix executable bits that will be
	// encoded in the Linux OCI layer. ELF validation below remains mandatory.
	if runtime.GOOS != "windows" && info.Mode()&0111 == 0 {
		return "", errors.New("binary is not executable")
	}
	if err := validateELFClosure(opts.Binary, opts.Architecture, opts.Profile, opts.ExtraFiles); err != nil {
		return "", err
	}
	if _, err := os.Stat(opts.Output); err == nil {
		return "", fmt.Errorf("output already exists: %s", opts.Output)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat output: %w", err)
	}

	observe(opts, "debug", "read-inputs", "input payload loaded", map[string]any{
		"binary_bytes": info.Size(), "extra_files": len(opts.ExtraFiles),
		"architecture": opts.Architecture, "os": opts.OS,
	}, time.Since(started))
	binaryMode := opts.BinaryMode
	if binaryMode == 0 {
		binaryMode = 0555
	}
	files := []streamFile{{
		dest: strings.TrimPrefix(opts.Entrypoint, "/"), source: opts.Binary, size: info.Size(), mode: binaryMode,
		category: CategoryApplication,
	}}
	if opts.PreserveBinaryOwnership {
		files[0].uid, files[0].gid = int(opts.BinaryUID), int(opts.BinaryGID)
	}
	uid, gid, _ := parseRuntimeUser(opts.User)
	if opts.Home == "" {
		opts.Home = "/home/nonroot"
	}
	if opts.IdentityFiles {
		files = append(files,
			withCategory(newInlineStreamFile("etc/passwd", fmt.Appendf(nil, "nonroot:x:%d:%d:nonroot:%s:/sbin/nologin\n", uid, gid, opts.Home), 0444), CategoryMetadata),
			withCategory(newInlineStreamFile("etc/group", fmt.Appendf(nil, "nonroot:x:%d:\n", gid), 0444), CategoryMetadata),
			withCategory(newInlineStreamFile("etc/nsswitch.conf", []byte("passwd: files\ngroup: files\nhosts: files dns\n"), 0444), CategoryMetadata),
		)
	}
	for _, extra := range opts.ExtraFiles {
		info, err := os.Stat(extra.Source)
		if err != nil {
			return "", fmt.Errorf("stat extra file %s: %w", extra.Dest, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("extra file %s source %s must be a regular file", extra.Dest, extra.Source)
		}
		mode := extra.Mode
		if mode == 0 {
			mode = 0555
		}
		category := extra.Category
		if category == "" {
			category = CategoryApplication
		}
		files = append(files, streamFile{
			dest: strings.TrimPrefix(extra.Dest, "/"), source: extra.Source,
			size: info.Size(), mode: mode, category: category,
			uid: ownershipValue(extra.PreserveOwnership, extra.UID), gid: ownershipValue(extra.PreserveOwnership, extra.GID),
		})
	}

	tmp, err := os.MkdirTemp(filepath.Dir(opts.Output), ".oci-layout-")
	if err != nil {
		return "", fmt.Errorf("create temporary layout: %w", err)
	}
	defer os.RemoveAll(tmp)
	if err := initializeLayout(tmp); err != nil {
		return "", err
	}
	var tracker *budget.Tracker
	if !opts.Budget.IsZero() {
		tracker = budget.NewTracker(opts.Budget)
		defer tracker.Stop()
	}
	layerDescs, diffIDs, err := writeLayers(tmp, files, opts, tracker)
	if err != nil {
		if resource, exceeded := budgetExceededResource(err); exceeded {
			observe(opts, "error", "layer", "build exceeded its resource budget", map[string]any{
				"resource": string(resource),
			}, time.Since(started))
		}
		return "", fmt.Errorf("write streaming layer: %w", err)
	}
	observe(opts, "debug", "layer", "deterministic layer(s) created", map[string]any{
		"files": len(files), "layers": len(layerDescs), "diff_ids": diffIDs,
		"copy_buffer_bytes": streamCopyBufferSize,
	}, time.Since(started))
	for _, tarPath := range opts.ExtraLayers {
		layerDesc, diffID, err := writePrebuiltLayer(tmp, tarPath, compressionLevel(opts.Compression))
		if err != nil {
			return "", fmt.Errorf("extra layer %s: %w", tarPath, err)
		}
		layerDescs = append(layerDescs, layerDesc)
		diffIDs = append(diffIDs, diffID)
		observe(opts, "debug", "extra-layer", "plugin-supplied layer validated and added", map[string]any{
			"source": tarPath, "digest": layerDesc.Digest, "diff_id": diffID,
		}, time.Since(started))
	}
	configBytes, err := makeConfig(opts, diffIDs)
	if err != nil {
		return "", err
	}
	configDesc := newDescriptor(configMediaType, configBytes)
	manifestBytes, err := json.Marshal(manifest{SchemaVersion: 2, Config: configDesc, Layers: layerDescs})
	if err != nil {
		return "", fmt.Errorf("encode manifest: %w", err)
	}
	manifestDesc := newDescriptor(manifestMediaType, manifestBytes)
	manifestDesc.Platform = &platform{Architecture: opts.Architecture, OS: opts.OS}
	manifestDesc.Annotations = map[string]string{
		"org.opencontainers.image.ref.name": opts.ImageName + ":" + opts.Tag,
	}
	observe(opts, "debug", "manifest", "OCI config and manifest encoded", map[string]any{
		"config_digest": configDesc.Digest, "manifest_digest": manifestDesc.Digest,
		"image_ref": opts.ImageName + ":" + opts.Tag,
	}, time.Since(started))
	indexBytes, err := json.Marshal(index{SchemaVersion: 2, Manifests: []descriptor{manifestDesc}})
	if err != nil {
		return "", fmt.Errorf("encode index: %w", err)
	}

	blobs := []blob{
		{digest: configDesc.Digest, data: configBytes},
		{digest: manifestDesc.Digest, data: manifestBytes},
	}
	if err := writeLayoutMetadata(tmp, indexBytes, blobs); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, opts.Output); err != nil {
		return "", fmt.Errorf("install layout: %w", err)
	}
	observe(opts, "info", "complete", "OCI layout installed atomically", map[string]any{
		"output": opts.Output, "manifest_digest": manifestDesc.Digest,
	}, time.Since(started))
	return manifestDesc.Digest, nil
}

func observe(opts Options, level, phase, message string, fields map[string]any, duration time.Duration) {
	if opts.Observer == nil {
		return
	}
	opts.Observer(Event{
		Time: time.Now().UTC(), Level: level, Component: "oci",
		Operation: "build", Phase: phase, TraceID: opts.TraceID,
		Message: message, Duration: duration, Fields: fields,
	})
}

func normalize(o *Options) error {
	if o.Binary == "" || o.Output == "" {
		return errors.New("binary and output are required")
	}
	if o.Architecture == "" {
		o.Architecture = "amd64"
	}
	if o.Architecture != "amd64" && o.Architecture != "arm64" {
		return fmt.Errorf("unsupported architecture %q (supported: amd64, arm64)", o.Architecture)
	}
	if o.OS == "" {
		o.OS = "linux"
	}
	if o.OS != "linux" {
		return fmt.Errorf("unsupported operating system %q (supported: linux)", o.OS)
	}
	if o.Entrypoint == "" {
		o.Entrypoint = "/app/service"
	}
	if !strings.HasPrefix(o.Entrypoint, "/") || path.Clean(o.Entrypoint) != o.Entrypoint || o.Entrypoint == "/" {
		return errors.New("entrypoint must be an absolute, clean container path")
	}
	seenDest := map[string]bool{o.Entrypoint: true}
	for _, extra := range o.ExtraFiles {
		if !strings.HasPrefix(extra.Dest, "/") || path.Clean(extra.Dest) != extra.Dest || extra.Dest == "/" {
			return fmt.Errorf("extra file destination must be an absolute, clean container path: %q", extra.Dest)
		}
		if seenDest[extra.Dest] {
			return fmt.Errorf("duplicate extra file destination %q", extra.Dest)
		}
		seenDest[extra.Dest] = true
		if extra.Category != "" && !validCategory(extra.Category) {
			return fmt.Errorf("extra file %q has unknown category %q (want one of: %s, or empty)",
				extra.Dest, extra.Category, strings.Join(semanticLayerOrder, ", "))
		}
		if extra.Mode < 0 || extra.Mode > 0o7777 {
			return fmt.Errorf("extra file %q has invalid mode %#o", extra.Dest, extra.Mode)
		}
		if extra.PreserveOwnership && (extra.UID > uint32(^uint32(0)>>1) || extra.GID > uint32(^uint32(0)>>1)) {
			return fmt.Errorf("extra file %q ownership exceeds portable tar limits", extra.Dest)
		}
	}
	if o.BinaryMode < 0 || o.BinaryMode > 0o7777 {
		return fmt.Errorf("binary mode %#o is invalid", o.BinaryMode)
	}
	if o.PreserveBinaryOwnership && (o.BinaryUID > uint32(^uint32(0)>>1) || o.BinaryGID > uint32(^uint32(0)>>1)) {
		return fmt.Errorf("binary ownership exceeds portable tar limits")
	}
	if o.Created.IsZero() {
		o.Created = time.Unix(0, 0).UTC()
	} else {
		o.Created = o.Created.UTC()
	}
	if o.ImageName == "" {
		o.ImageName = "platform-factory"
	}
	if o.Tag == "" {
		o.Tag = "latest"
	}
	if o.Labels == nil {
		o.Labels = map[string]string{}
	}
	if o.Profile == "" {
		o.Profile = "static"
	}
	if o.Compression == "" {
		o.Compression = "best"
	}
	if o.Compression != "best" && o.Compression != "fast" {
		return fmt.Errorf("unsupported compression %q (supported: best, fast)", o.Compression)
	}
	if err := (BuildConfig{
		Entrypoint: o.Entrypoint, Profile: o.Profile, Args: o.Args, WorkingDir: o.WorkingDir,
		Env: o.Env, User: o.User, Home: o.Home, IdentityFiles: o.IdentityFiles,
		Ports: o.Ports, Volumes: o.Volumes,
		WritablePaths: o.WritablePaths, Healthcheck: o.Healthcheck,
	}).Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(o.Output), 0755); err != nil {
		return fmt.Errorf("create output parent: %w", err)
	}
	return nil
}

type streamFile struct {
	dest     string
	source   string
	inline   []byte
	size     int64
	mode     int64
	category string
	uid      int
	gid      int
}

func ownershipValue(preserve bool, value uint32) int {
	if !preserve {
		return 0
	}
	return int(value)
}

func newInlineStreamFile(dest string, data []byte, mode int64) streamFile {
	return streamFile{dest: dest, inline: data, size: int64(len(data)), mode: mode}
}

func withCategory(file streamFile, category string) streamFile {
	file.category = category
	return file
}

// writeLayers writes files as a single layer (Options.SemanticLayers
// false, the default) or as one layer per non-empty category in
// semanticLayerOrder (true), returning the descriptors and diffIDs in
// layer order.
func writeLayers(root string, files []streamFile, opts Options, tracker *budget.Tracker) ([]descriptor, []string, error) {
	writablePaths := append(opts.WritablePaths, opts.Home)
	level := compressionLevel(opts.Compression)

	if !opts.SemanticLayers {
		layerDesc, diffID, err := writeStreamingLayer(root, files, writablePaths, level, tracker)
		if err != nil {
			return nil, nil, err
		}
		return []descriptor{layerDesc}, []string{diffID}, nil
	}

	grouped := make(map[string][]streamFile, len(semanticLayerOrder))
	for _, file := range files {
		category := file.category
		if category == "" {
			category = CategoryApplication
		}
		grouped[category] = append(grouped[category], file)
	}

	var layerDescs []descriptor
	var diffIDs []string
	for _, category := range semanticLayerOrder {
		group := grouped[category]
		if len(group) == 0 {
			continue
		}
		layerDesc, diffID, err := writeStreamingLayer(root, group, writablePaths, level, tracker)
		if err != nil {
			return nil, nil, fmt.Errorf("write %s layer: %w", category, err)
		}
		layerDescs = append(layerDescs, layerDesc)
		diffIDs = append(diffIDs, diffID)
	}
	return layerDescs, diffIDs, nil
}

// budgetExceededResource reports whether err wraps a *budget.BudgetExceededError
// and, if so, which resource was exceeded.
func budgetExceededResource(err error) (budget.ResourceType, bool) {
	if bee := budget.GetBudgetExceeded(err); bee != nil {
		return bee.Resource, true
	}
	return "", false
}

type countingWriter struct{ n int64 }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

func compressionLevel(value string) int {
	if value == "fast" {
		return gzip.BestSpeed
	}
	return gzip.BestCompression
}

func deterministicGzip(dst io.Writer, level int) (*gzip.Writer, error) {
	writer, err := gzip.NewWriterLevel(dst, level)
	if err == nil {
		writer.Header.ModTime = time.Unix(0, 0)
		writer.Header.OS = 255
	}
	return writer, err
}

func writeStreamingLayer(root string, files []streamFile, writablePaths []string, level int, tracker *budget.Tracker) (descriptor, string, error) {
	return installLayer(root, func(output io.Writer) (descriptor, string, error) {
		return writeLayerStream(output, files, writablePaths, level, tracker)
	})
}

func installLayer(root string, write func(io.Writer) (descriptor, string, error)) (descriptor, string, error) {
	blobDir := filepath.Join(root, "blobs", "sha256")
	output, err := os.CreateTemp(blobDir, ".layer-*")
	if err != nil {
		return descriptor{}, "", err
	}
	temporary := output.Name()
	success := false
	defer func() {
		_ = output.Close()
		if !success {
			_ = os.Remove(temporary)
		}
	}()

	layerDesc, diffID, err := write(output)
	if err != nil {
		return descriptor{}, "", err
	}
	if err := output.Close(); err != nil {
		return descriptor{}, "", err
	}
	destination := filepath.Join(blobDir, strings.TrimPrefix(layerDesc.Digest, "sha256:"))
	if err := os.Rename(temporary, destination); err != nil {
		return descriptor{}, "", err
	}
	if err := os.Chmod(destination, 0644); err != nil {
		return descriptor{}, "", err
	}
	success = true
	return layerDesc, diffID, nil
}

func writeLayerStream(dst io.Writer, files []streamFile, writablePaths []string, level int, tracker *budget.Tracker) (descriptor, string, error) {
	compressedHash := sha256.New()
	compressedCount := &countingWriter{}
	gz, err := deterministicGzip(io.MultiWriter(dst, compressedHash, compressedCount), level)
	if err != nil {
		return descriptor{}, "", err
	}
	rawHash := sha256.New()
	tw := tar.NewWriter(io.MultiWriter(gz, rawHash))

	directories := layerDirectories(files, writablePaths)
	for _, directory := range directories {
		mode := int64(0755)
		if directory == "tmp/" || directory == "var/tmp/" {
			mode = 01777
		}
		for _, writable := range writablePaths {
			if directory == strings.TrimPrefix(writable, "/")+"/" {
				mode = 0770
			}
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: directory, Typeflag: tar.TypeDir, Mode: mode,
			ModTime: time.Unix(0, 0), Format: tar.FormatPAX,
		}); err != nil {
			return descriptor{}, "", err
		}
	}
	sortedFiles := append([]streamFile(nil), files...)
	sort.Slice(sortedFiles, func(i, j int) bool { return sortedFiles[i].dest < sortedFiles[j].dest })
	copyBuffer := make([]byte, streamCopyBufferSize)
	for _, file := range sortedFiles {
		if tracker != nil {
			if resource, exceeded := tracker.Check(); exceeded {
				err := &budget.BudgetExceededError{Resource: resource, Budget: tracker.Budget(), Memory: tracker.CurrentMemory()}
				switch resource {
				case budget.ResourceTypeWallClock:
					err.Actual = tracker.WallClockElapsed()
				case budget.ResourceTypeCPU:
					err.Actual = tracker.CPUElapsed()
				}
				return descriptor{}, "", err
			}
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: file.dest, Mode: file.mode, Size: file.size, Uid: file.uid, Gid: file.gid,
			ModTime: time.Unix(0, 0), Format: tar.FormatPAX,
		}); err != nil {
			return descriptor{}, "", err
		}
		if err := copyStreamFile(tw, file, copyBuffer); err != nil {
			return descriptor{}, "", fmt.Errorf("stream %s: %w", file.dest, err)
		}
	}
	if err := tw.Close(); err != nil {
		return descriptor{}, "", err
	}
	if err := gz.Close(); err != nil {
		return descriptor{}, "", err
	}
	digest := "sha256:" + hex.EncodeToString(compressedHash.Sum(nil))
	return descriptor{MediaType: layerMediaType, Digest: digest, Size: compressedCount.n},
		"sha256:" + hex.EncodeToString(rawHash.Sum(nil)), nil
}

func layerDirectories(files []streamFile, writablePaths []string) []string {
	directories := map[string]bool{
		"app/": true, "etc/": true, "etc/ssl/": true, "etc/ssl/certs/": true,
		"tmp/": true, "var/": true, "var/tmp/": true,
	}
	for _, file := range files {
		current := ""
		for _, segment := range strings.Split(path.Dir(file.dest), "/") {
			if segment == "." {
				continue
			}
			current += segment + "/"
			directories[current] = true
		}
	}
	for _, writable := range writablePaths {
		current := ""
		for _, segment := range strings.Split(strings.TrimPrefix(writable, "/"), "/") {
			current += segment + "/"
			directories[current] = true
		}
	}
	result := make([]string, 0, len(directories))
	for directory := range directories {
		result = append(result, directory)
	}
	sort.Strings(result)
	return result
}

func copyStreamFile(dst io.Writer, file streamFile, buffer []byte) error {
	if len(buffer) == 0 {
		return errors.New("stream copy buffer must not be empty")
	}
	if file.source == "" {
		_, err := io.CopyBuffer(dst, bytes.NewReader(file.inline), buffer)
		return err
	}
	source, err := os.Open(file.source)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != file.size {
		return errors.New("source changed while the image was being built")
	}
	written, err := io.CopyBuffer(dst, io.LimitReader(source, file.size), buffer)
	if err != nil {
		return err
	}
	if written != file.size {
		return io.ErrUnexpectedEOF
	}
	var extra [1]byte
	if count, err := source.Read(extra[:]); count != 0 || err != io.EOF {
		return errors.New("source changed while the image was being built")
	}
	return nil
}

func makeConfig(o Options, diffIDs []string) ([]byte, error) {
	var c imageConfig
	c.Created = o.Created.Format(time.RFC3339)
	c.Architecture = o.Architecture
	c.OS = o.OS
	c.Config.User = o.User
	if c.Config.User == "" {
		c.Config.User = "65532:65532"
	}
	c.Config.Entrypoint = []string{o.Entrypoint}
	c.Config.Cmd = append([]string(nil), o.Args...)
	c.Config.WorkingDir = o.WorkingDir
	c.Config.Env = sortedEnv(o.Env)
	if o.Home != "" {
		c.Config.Env = append(c.Config.Env, "HOME="+o.Home)
		sort.Strings(c.Config.Env)
	}
	if len(o.Ports) > 0 {
		c.Config.ExposedPorts = map[string]struct{}{}
		for _, item := range o.Ports {
			c.Config.ExposedPorts[item] = struct{}{}
		}
	}
	if len(o.Volumes) > 0 {
		c.Config.Volumes = map[string]struct{}{}
		for _, item := range o.Volumes {
			c.Config.Volumes[item] = struct{}{}
		}
	}
	if o.Healthcheck != nil {
		health := &imageHealthcheck{
			Test:    append([]string{"CMD"}, o.Healthcheck.Command...),
			Retries: o.Healthcheck.Retries,
		}
		if o.Healthcheck.Interval != "" {
			duration, _ := time.ParseDuration(o.Healthcheck.Interval)
			health.Interval = int64(duration)
		}
		if o.Healthcheck.Timeout != "" {
			duration, _ := time.ParseDuration(o.Healthcheck.Timeout)
			health.Timeout = int64(duration)
		}
		c.Config.Healthcheck = health
	}
	c.Config.Labels = copyLabels(o.Labels)
	c.RootFS = rootFS{Type: "layers", DiffIDs: diffIDs}
	return json.Marshal(c)
}
func copyLabels(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func newDescriptor(media string, data []byte) descriptor {
	d := sha256.Sum256(data)
	return descriptor{MediaType: media, Digest: "sha256:" + hex.EncodeToString(d[:]), Size: int64(len(data))}
}

// blob pairs blob content with its already-computed "sha256:<hex>" digest
// (from newDescriptor), so writeLayout never has to hash a large layer a
// second time just to pick its content-addressed filename.
type blob struct {
	digest string
	data   []byte
}

func writeLayout(root string, idx []byte, blobs []blob) error {
	if err := initializeLayout(root); err != nil {
		return err
	}
	return writeLayoutMetadata(root, idx, blobs)
}

func initializeLayout(root string) error {
	if err := os.MkdirAll(filepath.Join(root, "blobs", "sha256"), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "oci-layout"), []byte("{\"imageLayoutVersion\":\"1.0.0\"}\n"), 0644); err != nil {
		return err
	}
	return nil
}

func writeLayoutMetadata(root string, idx []byte, blobs []blob) error {
	if err := os.WriteFile(filepath.Join(root, "index.json"), append(idx, '\n'), 0644); err != nil {
		return err
	}
	for _, b := range blobs {
		if !strings.HasPrefix(b.digest, "sha256:") || len(b.digest) != len("sha256:")+sha256.Size*2 {
			return fmt.Errorf("invalid blob digest %q", b.digest)
		}
		hexDigest := strings.TrimPrefix(b.digest, "sha256:")
		if _, err := hex.DecodeString(hexDigest); err != nil {
			return fmt.Errorf("invalid blob digest %q: %w", b.digest, err)
		}
		if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", hexDigest), b.data, 0644); err != nil {
			return err
		}
	}
	return nil
}

// LabelsFromPairs parses key=value labels and rejects ambiguous input.
func LabelsFromPairs(pairs []string) (map[string]string, error) {
	labels := make(map[string]string, len(pairs))
	sorted := append([]string(nil), pairs...)
	sort.Strings(sorted)
	for _, pair := range sorted {
		k, v, ok := strings.Cut(pair, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid label %q (expected key=value)", pair)
		}
		if _, exists := labels[k]; exists {
			return nil, fmt.Errorf("duplicate label %q", k)
		}
		labels[k] = v
	}
	return labels, nil
}

// ExtraFilesFromPairs parses "[CATEGORY@]/container/path=host/path" pairs
// (as passed via repeated -extra-file flags) into ExtraFiles, and rejects
// ambiguous or colliding input. The optional CATEGORY prefix assigns the
// file to a semantic layer (toolchain, dependencies, application or
// metadata) and only takes effect when Options.SemanticLayers is set. It
// only validates the pair's string shape, category name and
// destination-path syntax; Build validates that each Source actually
// exists and is a regular file, since that requires filesystem access.
func ExtraFilesFromPairs(pairs []string) ([]ExtraFile, error) {
	sorted := append([]string(nil), pairs...)
	sort.Strings(sorted)
	seen := make(map[string]bool, len(sorted))
	files := make([]ExtraFile, 0, len(sorted))
	for _, pair := range sorted {
		dest, source, ok := strings.Cut(pair, "=")
		if !ok || dest == "" || source == "" {
			return nil, fmt.Errorf("invalid extra file %q (expected [CATEGORY@]/container/path=host/path)", pair)
		}
		category := ""
		if !strings.HasPrefix(dest, "/") {
			prefix, remainder, tagged := strings.Cut(dest, "@")
			if !tagged || !validCategory(prefix) {
				return nil, fmt.Errorf("extra file destination must be an absolute, clean container path, optionally prefixed by one category of %s followed by %q: %q",
					strings.Join(semanticLayerOrder, ", "), "@", dest)
			}
			category, dest = prefix, remainder
		}
		if !strings.HasPrefix(dest, "/") || path.Clean(dest) != dest || dest == "/" {
			return nil, fmt.Errorf("extra file destination must be an absolute, clean container path: %q", dest)
		}
		if seen[dest] {
			return nil, fmt.Errorf("duplicate extra file destination %q", dest)
		}
		seen[dest] = true
		files = append(files, ExtraFile{Dest: dest, Source: source, Category: category})
	}
	return files, nil
}
