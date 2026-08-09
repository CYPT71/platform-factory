// Package oci is the supported SDK for building deterministic OCI Image
// Layouts natively, without Docker, Podman, or BuildKit.
//
// A layout produced by Build is a complete, standard OCI Image Layout
// directory: it needs no secure-oci code to run as an ordinary container
// under Docker, Podman, or containerd. Optionally targeting the secure-oci
// MicroVM runtime for the same layout is a separate, later step - see
// sdk/microvm - not a different build.
package oci
