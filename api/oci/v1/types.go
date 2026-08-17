package v1

import (
	"time"

	"github.com/CYPT71/platform-factory/internal/oci"
)

// BuildOptions describes the image to create. This is the public API
// contract for OCI image building. Binary must name a regular executable
// file. Output must not already exist; this prevents accidentally replacing an
// image layout with attacker-controlled contents.
type BuildOptions struct {
	// Binary is the path to the executable file to include in the image.
	Binary string
	// Output is the directory where the OCI layout will be written.
	Output string
	// Architecture is the target CPU architecture (e.g., "amd64", "arm64").
	Architecture string
	// OS is the target operating system (e.g., "linux", "darwin").
	OS string
	// Entrypoint is the absolute path to the executable within the container.
	Entrypoint string
	// Profile selects the build profile (e.g., "static", "glibc", "musl").
	Profile string
	// ImageName is the name for the image (used in manifest annotations).
	ImageName string
	// Tag is the tag for the image (used in manifest annotations).
	Tag string
	// Created is the creation timestamp for the image.
	Created time.Time
	// Labels are key-value pairs added to the image configuration.
	Labels map[string]string
	// ExtraFiles are additional files to include in the image layers.
	ExtraFiles []ExtraFile
	// Args are the default arguments for the entrypoint.
	Args []string
	// WorkingDir is the working directory for the container.
	WorkingDir string
	// Env contains environment variables for the container.
	Env map[string]string
	// User specifies the user to run as (UID or UID:GID).
	User string
	// Home specifies the home directory for the user.
	Home string
	// IdentityFiles, if true, includes /etc/passwd, /etc/group, and /etc/nsswitch.conf.
	IdentityFiles bool
	// Ports are the container ports to expose.
	Ports []string
	// Volumes are the mount points for external volumes.
	Volumes []string
	// WritablePaths are paths that should be writable in the container.
	WritablePaths []string
	// Healthcheck configures the container health check.
	Healthcheck *Healthcheck
	// Compression selects the gzip compression level ("best" or "fast").
	Compression string
	// SemanticLayers, if true, splits the image into multiple layers by semantic category.
	SemanticLayers bool
	// TraceID correlates build events across CLI and CI. Metadata only, not written to layout.
	TraceID string
	// Observer receives structured lifecycle events during the build.
	Observer func(BuildEvent)
	// ExtraLayers are paths to pre-built tar files to include as additional layers.
	ExtraLayers []string
}

// BuildEvent is a structured, non-secret observation of an OCI build phase.
type BuildEvent struct {
	Time      time.Time              `json:"time"`
	Level     string                 `json:"level"`
	Component string                 `json:"component"`
	Operation string                 `json:"operation"`
	Phase     string                 `json:"phase"`
	TraceID   string                 `json:"trace_id,omitempty"`
	Message   string                 `json:"message"`
	Duration  time.Duration          `json:"-"`
	Fields    map[string]interface{} `json:"fields,omitempty"`
}

// ExtraFile places an additional file in the layer at a fixed container path.
type ExtraFile struct {
	// Dest is the absolute, clean container path this file is written to.
	Dest string
	// Source is the host path its content is read from at build time.
	Source string
	// Mode is the file permissions (e.g., 0555 for executables, 0444 for data).
	Mode int64
	// Category groups this file into a semantic layer when SemanticLayers is enabled.
	Category string
}

// Healthcheck configures the container health check.
type Healthcheck struct {
	// Command is the command to run to check health.
	Command []string
	// Interval is the time between checks (Go duration string).
	Interval string
	// Timeout is the maximum time allowed for the check (Go duration string).
	Timeout string
	// Retries is the number of consecutive failures allowed before marking as unhealthy.
	Retries int
}

// Semantic layer categories for BuildOptions.SemanticLayers.
const (
	CategoryToolchain    = "toolchain"
	CategoryDependencies = "dependencies"
	CategoryApplication  = "application"
	CategoryMetadata     = "metadata"
)

// toInternalOptions converts public API BuildOptions to internal oci.Options.
func toInternalOptions(opts BuildOptions) oci.Options {
	return oci.Options{
		Binary:         opts.Binary,
		Output:         opts.Output,
		Architecture:   opts.Architecture,
		OS:             opts.OS,
		Entrypoint:     opts.Entrypoint,
		Profile:        opts.Profile,
		ImageName:      opts.ImageName,
		Tag:            opts.Tag,
		Created:        opts.Created,
		Labels:         opts.Labels,
		ExtraFiles:     toInternalExtraFiles(opts.ExtraFiles),
		Args:           opts.Args,
		WorkingDir:     opts.WorkingDir,
		Env:            opts.Env,
		User:           opts.User,
		Home:           opts.Home,
		IdentityFiles:  opts.IdentityFiles,
		Ports:          opts.Ports,
		Volumes:        opts.Volumes,
		WritablePaths:  opts.WritablePaths,
		Healthcheck:    toInternalHealthcheck(opts.Healthcheck),
		Compression:    opts.Compression,
		SemanticLayers: opts.SemanticLayers,
		TraceID:        opts.TraceID,
		Observer:       toInternalObserver(opts.Observer),
		ExtraLayers:    opts.ExtraLayers,
	}
}

// toInternalExtraFiles converts public ExtraFile slice to internal.
func toInternalExtraFiles(files []ExtraFile) []oci.ExtraFile {
	if len(files) == 0 {
		return nil
	}
	result := make([]oci.ExtraFile, len(files))
	for i, f := range files {
		result[i] = oci.ExtraFile{
			Dest:     f.Dest,
			Source:   f.Source,
			Mode:     f.Mode,
			Category: f.Category,
		}
	}
	return result
}

// toInternalHealthcheck converts public Healthcheck to internal.
func toInternalHealthcheck(h *Healthcheck) *oci.Healthcheck {
	if h == nil {
		return nil
	}
	return &oci.Healthcheck{
		Command:  append([]string(nil), h.Command...),
		Interval: h.Interval,
		Timeout:  h.Timeout,
		Retries:  h.Retries,
	}
}

// toInternalObserver converts public observer function to internal.
func toInternalObserver(obs func(BuildEvent)) func(oci.Event) {
	if obs == nil {
		return nil
	}
	return func(e oci.Event) {
		obs(BuildEvent{
			Time:      e.Time,
			Level:     e.Level,
			Component: e.Component,
			Operation: e.Operation,
			Phase:     e.Phase,
			TraceID:   e.TraceID,
			Message:   e.Message,
			Duration:  e.Duration,
			Fields:    e.Fields,
		})
	}
}

// Build writes an OCI Image Layout and returns the digest of its manifest.
func Build(opts BuildOptions) (string, error) {
	internalOpts := toInternalOptions(opts)
	return oci.Build(internalOpts)
}
