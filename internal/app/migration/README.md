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
