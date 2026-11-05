# Migration application orchestration

This package resolves independently verified capabilities and orchestrates
migration use cases through inward-facing ports. Canonical business invariants
and deterministic planning belong to `internal/migration`; this package does
not pair implementations or depend on the concrete plugin registry.

The similarly named types under `api/migration/v1` are stable transport DTOs, not
a second business model. Conversion is owned by outer adapters; the domain and
application layers never import the public transport package.

`RoundTripValidator` composes the complete host-controlled proof path: source
discovery, deterministic planning, verified execution, target rediscovery, and
semantic graph comparison. Provider-native origins may change; resource IDs,
kinds, attributes, requirements, and dependency edges may not. Incomplete
discovery or any semantic drift fails closed.

## Resolution and execution

`CapabilityResolver` evaluates independently verified evidence before selecting
an implementation. A declared capability alone is insufficient. Outer adapters
bind the selected implementation through `TargetOperationsFactory`; the use
case owns observe/apply/verify semantics and emits provenance through its sink.

`Executor.Execute` performs the one-shot path. `Executor.Reconcile` adds
bounded observe/difference/action cycles through `ProgressOperations`.
Reconciliation defaults to eight iterations, requires a known difference,
and requires strictly decreasing distance after an action. Unknown state,
repeated fingerprints, absent progress or the iteration limit stop the step.
Dependent steps are blocked when their prerequisites fail. Both paths require
a trace ID in the context and validate the plan before provider operations.

## Artifact boundary

`ArtifactTransfer` verifies the exported artifact and the content stored in CAS
before import. Classification, size/digest checks and the sentinel secret
defense are separate checks; a plugin's success response does not establish
that its bytes are safe to install. See `artifact.go` and its rejection tests.

These application services are not themselves CLI commands or proof that a
particular source/target provider is available. That requires the corresponding
adapter, verified capability evidence and runtime grants.
