// Package provenance builds native build provenance records: the complete
// pipeline DAG, its declared inputs, resolved output digests, and
// caller-supplied build metadata.
//
// This does not auto-detect a source repository or commit (that would
// mean either shelling out to git or writing a .git ref parser — a
// separate capability), does not record toolchain digests (nothing
// currently resolves/pins one) or plugin invocations (nothing currently
// records which plugins ran). Those fields stay caller-suppliable but are
// never auto-populated.
package provenance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/CYPT71/platform-factory/internal/assemble"
	api "github.com/CYPT71/platform-factory/internal/core"
	"github.com/CYPT71/platform-factory/internal/pipeline"
)

// Options carries the build metadata a caller supplies; none of it is
// auto-detected.
type Options struct {
	BuilderIdentity string
	Source          string
	Commit          string
	Parameters      map[string]string
	Generated       time.Time
}

// OutputRecord is one resolved pipeline output's provenance entry.
type OutputRecord struct {
	Name     string `json:"name"`
	Stage    string `json:"stage"`
	Artifact string `json:"artifact"`
	Digest   string `json:"digest"`
}

// Record is a native build provenance record. Pipeline is embedded in
// full: its Stage.Secrets already carry only identities, never values, so
// embedding it cannot leak secret material.
type Record struct {
	BuilderIdentity string            `json:"builder_identity"`
	Source          string            `json:"source,omitempty"`
	Commit          string            `json:"commit,omitempty"`
	Parameters      map[string]string `json:"parameters,omitempty"`
	Generated       time.Time         `json:"generated,omitempty"`
	Pipeline        api.Pipeline      `json:"pipeline"`
	Order           []string          `json:"order"`
	Outputs         []OutputRecord    `json:"outputs"`
}

// Generate validates definition's DAG, resolves every declared output's
// digest via resolve, and combines them with opts into a Record.
func Generate(definition api.Pipeline, resolve assemble.OutputResolver, opts Options) (Record, error) {
	if opts.BuilderIdentity == "" {
		return Record{}, errors.New("provenance: builder identity is required")
	}
	graph, err := pipeline.Analyze(definition)
	if err != nil {
		return Record{}, fmt.Errorf("provenance: %w", err)
	}

	outputs := make([]OutputRecord, 0, len(definition.Outputs))
	for _, output := range definition.Outputs {
		descriptor, ok := resolve(output.Stage, output.Artifact)
		if !ok {
			return Record{}, fmt.Errorf("provenance: output %q: stage %q has not produced artifact %q", output.Name, output.Stage, output.Artifact)
		}
		outputs = append(outputs, OutputRecord{
			Name: output.Name, Stage: output.Stage, Artifact: output.Artifact, Digest: descriptor.Digest,
		})
	}

	return Record{
		BuilderIdentity: opts.BuilderIdentity,
		Source:          opts.Source,
		Commit:          opts.Commit,
		Parameters:      opts.Parameters,
		Generated:       opts.Generated,
		Pipeline:        definition,
		Order:           graph.Order,
		Outputs:         outputs,
	}, nil
}

// Write encodes record as JSON to w.
func Write(w io.Writer, record Record) error {
	return json.NewEncoder(w).Encode(record)
}
