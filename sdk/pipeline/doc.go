// Package pipeline is the supported SDK for loading and inspecting secure-oci
// pipeline definitions.
//
// Applications combine this package with the versioned wire types in api/v1.
// Decode applies the same strict, size-bounded validation as the secure-oci
// command line tools: unknown fields, trailing JSON values, invalid
// references, and dependency cycles are rejected before a definition is used.
package pipeline
