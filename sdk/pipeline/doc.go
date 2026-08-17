// Package pipeline loads and validates pipeline definitions.
//
// Applications combine this package with the versioned wire types in
// api/v1alpha1.
// Decode applies the same strict, size-bounded validation as the platform-factory
// command line: unknown fields, trailing values, invalid references, and
// dependency cycles are rejected before use.
package pipeline
