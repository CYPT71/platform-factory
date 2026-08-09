// Package oci provides the public API for OCI image building.
//
// This package is the stable public interface for creating OCI image layouts.
// It delegates to the internal implementation while maintaining API stability.
//
// See Sanetizer-todo.md phase 4, items 9-10: SDK packages must not import
// internal packages directly.
package oci

import (
	api "github.com/CYPT71/secure-oci-base/api/oci"
)

// Options describes the image to create. See api.oci.BuildOptions for the
// full field-by-field contract.
type Options = api.BuildOptions

// Event is a structured, non-secret observation of a build phase, delivered
// to Options.Observer.
type Event = api.BuildEvent

// ExtraFile places an additional file in the layer at a fixed container
// path, alongside the entrypoint.
type ExtraFile = api.ExtraFile

// Healthcheck is the OCI image config healthcheck to embed, if any.
type Healthcheck = api.Healthcheck

// Layer categories for Options.SemanticLayers.
const (
	CategoryToolchain    = api.CategoryToolchain
	CategoryDependencies = api.CategoryDependencies
	CategoryApplication  = api.CategoryApplication
	CategoryMetadata     = api.CategoryMetadata
)

// Build writes an OCI Image Layout to Options.Output and returns the digest
// of its manifest. The result is a complete, standard OCI Image Layout: it
// runs as an ordinary container under any OCI-compatible engine with no
// further secure-oci involvement. Targeting the MicroVM runtime for the
// same layout instead is opt-in and happens later, via sdk/microvm - Build
// itself never depends on it and never requires it.
func Build(opts Options) (digest string, err error) {
	return api.Build(api.BuildOptions(opts))
}
