// Package pipeline exposes stable helpers for public pipeline DTOs.
package pipeline

import (
	"io"

	api "github.com/CYPT71/secure-oci-base/api/pipeline"
	v1 "github.com/CYPT71/secure-oci-base/api/v1"
)

type Issue = api.Issue
type ValidationError = api.ValidationError
type Graph = api.Graph

func Decode(input io.Reader) (v1.Pipeline, Graph, error) { return api.Decode(input) }
func Analyze(definition v1.Pipeline) (Graph, error)      { return api.Analyze(definition) }
func CanonicalJSON(definition v1.Pipeline) ([]byte, error) {
	return api.CanonicalJSON(definition)
}
func Fingerprint(definition v1.Pipeline) (string, error) { return api.Fingerprint(definition) }
