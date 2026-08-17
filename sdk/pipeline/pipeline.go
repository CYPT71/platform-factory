// Package pipeline exposes stable helpers for public pipeline DTOs.
package pipeline

import (
	"io"

	api "github.com/CYPT71/platform-factory/api/pipeline/v1"
)

type Issue = api.Issue
type ValidationError = api.ValidationError
type Graph = api.Graph

func Decode(input io.Reader) (api.Pipeline, Graph, error) { return api.Decode(input) }
func Analyze(definition api.Pipeline) (Graph, error)      { return api.Analyze(definition) }
func CanonicalJSON(definition api.Pipeline) ([]byte, error) {
	return api.CanonicalJSON(definition)
}
func Fingerprint(definition api.Pipeline) (string, error) { return api.Fingerprint(definition) }
