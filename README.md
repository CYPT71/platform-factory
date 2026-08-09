# Platform Factory

> **One platform to understand, transform, and operate workloads across heterogeneous environments.**

Platform Factory connects **existing systems and modern infrastructure** through a canonical model and a **bidirectional plugin architecture**.

```text id="bidir"
     SOURCE ENVIRONMENT
 Code / VM / Legacy / Cloud
            │
            ▼
      Source Plugin
   discover / observe
            │
            ▼
     CANONICAL MODEL
            │
      plan / validate
      build / secure
            │
            ▼
      Target Plugin
   apply / observe
            │
            ▼
     TARGET ENVIRONMENT
 Kubernetes / Container /
 MicroVM / Cloud / Runtime
            │
            └──────────────► observed state
                              │
                              └────► Platform Factory
```

Plugins work in **both directions**:

```text id="pluginflow"
Environment → Plugin → Platform Factory
Platform Factory → Plugin → Environment
```

They expose **capabilities**, not hard-coded integrations.

```text id="caps"
discover
observe
build
runtime.create
deployment.apply
storage.import
network.configure
```

Platform Factory resolves the required capabilities dynamically.

## One workflow

```text id="workflow"
pf init      → understand & model
pf build     → transform & verify
pf publish   → publish & apply
              ↓
           observe
              ↓
           reconcile
```

## Why

Organizations operate generations of technology simultaneously.

Platform Factory provides:

* one canonical workload model;
* deterministic OCI artifacts;
* SBOM, provenance and signatures;
* policy and verification;
* containers and MicroVMs;
* bidirectional plugins;
* migration between heterogeneous environments;
* continuous observation and reconciliation.

## The difference

Platform Factory is not another builder, runtime, or Kubernetes wrapper.

It creates a common control layer between **where software comes from** and **where it needs to run**.

Adding a new environment means implementing its capabilities — not integrating it separately with every other environment.

```text id="final"
Any supported source
        ↕
      Plugin
        ↕
Platform Factory
        ↕
      Plugin
        ↕
Any supported target
```

> **One model. One lifecycle. Any supported source ↔ any supported target.**
