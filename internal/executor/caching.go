package executor

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/CYPT71/platform-factory/internal/core"
)

// CachingRunner wraps a StageRunner with a content-addressed
// cache: a stage whose computed cache.StageKey already has a verified
// record is skipped instead of re-run, and a stage that does run has its
// declared outputs stored and recorded under that key for later reuse.
//
// Declared outputs are resolved to local files via MapPath(root, ...), the
// same convention Executor uses, so a CachingRunner commonly wraps an
// Executor sharing the same root.
type CachingRunner struct {
	inner  StageRunner
	root   string
	store  core.CacheStore
	engine string
	base   string
	target string

	mu      sync.Mutex
	outputs map[string]map[string]core.Descriptor
	hits    []string
}

// NewCachingRunner returns a CachingRunner. engineVersion, baseDigest and
// platform are passed through to every cache.StageKey computation; see
// core.CacheStageKeyInputs.
func NewCachingRunner(inner StageRunner, root string, store core.CacheStore, engineVersion, baseDigest, platform string) *CachingRunner {
	return &CachingRunner{
		inner: inner, root: root, store: store,
		engine: engineVersion, base: baseDigest, target: platform,
		outputs: map[string]map[string]core.Descriptor{},
	}
}

// Output returns the content descriptor stage produced for artifact,
// whether from a fresh run or a replayed cache hit. ok is false if stage
// has not run (via this CachingRunner) yet or did not declare that
// artifact as an output.
func (c *CachingRunner) Output(stage, artifact string) (core.Descriptor, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	descriptor, ok := c.outputs[stage][artifact]
	return descriptor, ok
}

// Hits returns the IDs of stages skipped because a verified cache record
// already existed, in the order they were skipped.
func (c *CachingRunner) Hits() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.hits...)
}

// Run satisfies StageRunner.
func (c *CachingRunner) Run(ctx context.Context, stage core.Stage) error {
	digests, err := c.inputDigests(stage)
	if err != nil {
		return err
	}
	key, err := c.store.StageKey(core.CacheStageKeyInputs{
		EngineVersion: c.engine,
		Stage:         stage,
		BaseDigest:    c.base,
		InputDigests:  digests,
		Platform:      c.target,
	})
	if err != nil {
		return fmt.Errorf("caching runner: stage %q: cache key: %w", stage.ID, err)
	}

	if record, ok := c.validRecord(key); ok {
		c.setOutputs(stage.ID, record)
		c.recordHit(stage.ID)
		return nil
	}

	if err := c.inner.Run(ctx, stage); err != nil {
		return err
	}
	// A stage that received secret material is tainted. Its outputs and result
	// record must never enter the persistent CAS, even if the command itself
	// promises not to copy the secret. This fail-closed rule also prevents a
	// later cache hit from replaying an output whose non-sensitivity was never
	// proven.
	if len(stage.Secrets) > 0 {
		return nil
	}

	produced, err := c.putOutputs(stage)
	if err != nil {
		return err
	}
	if err := c.store.PutRecord(key, produced); err != nil {
		return fmt.Errorf("caching runner: stage %q: write record: %w", stage.ID, err)
	}
	c.setOutputs(stage.ID, produced)
	return nil
}

// validRecord looks up key and re-verifies every referenced blob so a
// corrupted or partially garbage-collected cache entry is treated as a
// miss rather than served or reported as an error.
func (c *CachingRunner) validRecord(key string) (map[string]core.Descriptor, bool) {
	var record map[string]core.Descriptor
	found, err := c.store.GetRecord(key, &record)
	if err != nil || !found {
		return nil, false
	}
	for _, descriptor := range record {
		if err := c.store.Verify(descriptor.Digest); err != nil {
			return nil, false
		}
	}
	return record, true
}

func (c *CachingRunner) inputDigests(stage core.Stage) ([]string, error) {
	if len(stage.Inputs) == 0 {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	digests := make([]string, len(stage.Inputs))
	for index, reference := range stage.Inputs {
		producer, ok := c.outputs[reference.Stage]
		if !ok {
			return nil, fmt.Errorf("caching runner: stage %q: dependency %q has not produced outputs yet", stage.ID, reference.Stage)
		}
		descriptor, ok := producer[reference.Name]
		if !ok {
			return nil, fmt.Errorf("caching runner: stage %q: dependency %q did not produce artifact %q", stage.ID, reference.Stage, reference.Name)
		}
		digests[index] = descriptor.Digest
	}
	return digests, nil
}

func (c *CachingRunner) putOutputs(stage core.Stage) (map[string]core.Descriptor, error) {
	produced := make(map[string]core.Descriptor, len(stage.Outputs))
	for _, output := range stage.Outputs {
		path := MapPath(c.root, output.Path)
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("caching runner: stage %q: output %q: %w", stage.ID, output.Name, err)
		}
		descriptor, err := c.store.Put(file)
		_ = file.Close()
		if err != nil {
			return nil, fmt.Errorf("caching runner: stage %q: cache output %q: %w", stage.ID, output.Name, err)
		}
		produced[output.Name] = descriptor
	}
	return produced, nil
}

func (c *CachingRunner) setOutputs(stageID string, outputs map[string]core.Descriptor) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.outputs[stageID] = outputs
}

func (c *CachingRunner) recordHit(stageID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hits = append(c.hits, stageID)
}
