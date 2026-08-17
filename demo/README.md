# Platform Factory MVP demo

This demo proves the smallest complete user outcome from a clean project:

```text
main.go → pf init → verified OCI layout → Docker/Podman → hello from pf mvp
```

It builds the real CLI and the real Go language plugin from this checkout. The
application workspace itself starts with only `hello-world/main.go`; no
pre-generated `pf.yaml`, lock file, container file, or OCI layout is used.

## Try PF yourself

For a live presentation, three copy/paste-ready scripts are available:

- `demo-junior.txt`: install from a relative path, then initialize Go, Python,
  JavaScript, and TypeScript without manually loading a language plugin;
- `demo-intermediaire.txt`: initialize and run all four stacks, with a complete
  verified OCI/SBOM/evidence path for Go and an explicit interpreted-runtime
  limitation for Python/Node rather than a fake successful image;
- `demo-senior.txt`: audit all four projects, then plan and run an
  automatically sandboxed parallel pipeline.

Run the interactive workshop from the repository root:

```bash
./demo/try-pf.sh docker
# or
./demo/try-pf.sh podman
```

The launcher opens an isolated shell in a new repository containing only
`main.go`. It invites you to type the real PF commands yourself:

```bash

pf init --engine docker .
pf launch
pf inspect .
pf plugin list
```

This is intentionally not a prerecorded simulation: you control the TUI,
review its proposal and decide whether to write the project. Type `exit` to
leave. The temporary workspace path is printed and retained so you can inspect
the generated configuration and OCI layout afterward.

## Automated acceptance

The non-interactive equivalent used by CI is:

```bash
./demo/validate.sh docker
./demo/validate.sh podman
./demo/validate-personas.sh
./demo/validate-demo-stacks.sh
```

The selected engine must already be installed and running. PF deliberately
does not start Docker or a Podman machine. The script fails rather than skips
when a prerequisite or acceptance assertion is missing.

Successful validation proves:

- language detection is provided by the loaded Go plugin;
- dry-run performs no write;
- init creates `pf.yaml` and `pf.lock` beside the source, plus the gitignored
  `.pf/inventory.json` and validated `.pf/build.pipeline.json` inspection
  evidence;
- `--filename-style long` creates the equally supported, single-source pair
  `platform-factory.yaml`/`platform-factory.lock` and normal discovery loads it;
- `pf launch` builds and verifies a local OCI layout;
- the layout remains at `.platform-factory/image`;
- the selected local engine runs the image and prints `hello from pf mvp`.

`validate-personas.sh` additionally proves three clean-workspace paths:

- a junior starts with one source file, reviews an initialization with zero
  writes, initializes, builds, verifies the OCI layout, and receives SBOM and
  build reports through the product-level `dist/` and `reports/` contract;
- an intermediate author consumes the Python SDK, passes the common plugin
  conformance suite and writes a verified OCI image;
- a senior operator plans and executes a parallel pipeline with mounted source,
  cache, trace, journal, automatic sandbox selection, and a runnable final
  artifact. The acceptance never disables sandboxing explicitly; unsupported
  hosts report the visible `auto` fallback while Linux CI exercises isolation.

This is the current MVP boundary. Python/Node/.NET plugin inspection and native
Hello World experiences are tested elsewhere, but a verified Linux runtime
layer for their OCI launch is still an explicit open item.
