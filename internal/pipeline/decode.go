package pipeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	api "github.com/CYPT71/platform-factory/internal/core"
)

const maxDefinitionBytes = 1 << 20

// Decode reads one strict, size-bounded JSON pipeline and validates its DAG.
// Unknown fields and trailing JSON values are rejected.
func Decode(input io.Reader) (api.Pipeline, Graph, error) {
	limited := io.LimitReader(input, maxDefinitionBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return api.Pipeline{}, Graph{}, fmt.Errorf("read pipeline: %w", err)
	}
	if len(data) > maxDefinitionBytes {
		return api.Pipeline{}, Graph{}, errors.New("pipeline exceeds the 1 MiB definition limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var definition api.Pipeline
	if err := decoder.Decode(&definition); err != nil {
		return api.Pipeline{}, Graph{}, fmt.Errorf("decode pipeline: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return api.Pipeline{}, Graph{}, errors.New("pipeline must contain exactly one JSON object")
	}
	graph, err := Analyze(definition)
	if err != nil {
		return api.Pipeline{}, Graph{}, err
	}
	return definition, graph, nil
}
