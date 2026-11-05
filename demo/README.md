# Platform Factory MVP demo

This demo proves the smallest complete user outcome from a clean project:

```text
main.go → pf init → verified OCI layout → Docker/Podman → hello from pf mvp
```

It builds the real CLI and the real Go language plugin from this checkout. The
application workspace itself starts with only `hello-world/main.go`; no
pre-generated `pf.yaml`, lock file, container file, or OCI layout is used.

## Recorded terminal demo

The [README animation](../docs/assets/from-zero.gif) was recorded on macOS/arm64
using the actual CLI and Python language plugin built from this checkout.
Commands were entered individually in an interactive Bash terminal, including
confirmation of the initialization plan and the runtime selection in the TUI.
The application folder started empty. No demo script executed the sequence and
no command output was substituted. Only idle pauses were shortened in the GIF;
the [original recording](../docs/assets/from-zero.cast) retains output and timing.

Prerequisites: install PF with its Python plugin, start Podman, and allow access
to the registry for the explicitly selected runtime. Installation and Podman
startup happen before recording. The target is Linux/amd64; an ARM64 Podman
machine needs AMD64 execution support. Use a unique project directory so its
image reference does not collide with an existing local image.

From an empty directory, type these commands individually:

```bash
ls -A
cat > app.py <<'PYTHON'
print("Hello from Python!")
PYTHON
pf init --engine podman .
```

Review the plan: only `pf.yaml` and `pf.lock` are added. Confirm with `y`.
In the runtime TUI, select **Pull a base image**, then enter the immutable
reference used in this recording:

```text
python@sha256:05a994d2344eb364cc186910f4fc8786a38215f39f00d0acc25bff33a08f4177
```

This explicitly downloads and stages the Python interpreter and its available
shared libraries under `.platform-factory/`. The slim base lacks Tk libraries;
the plugin reports and excludes the unusable `_tkinter` extension. The demo
uses a dependency-free script and does not prove arbitrary package support.
Builds do not fetch the interpreter implicitly. Then continue:

```bash
clear
ls -A
pf launch
clear
pf verify --format text .platform-factory/image
pf status --format text .
ls -A
```

`pf launch` builds the OCI image and runs the program, printing
`Hello from Python!`. Verification checks the retained layout. The final folder
contains only `app.py`, `pf.yaml`, `pf.lock` and `.platform-factory/`; the last
folder holds the staged runtime, freeze/build state, evidence and image.
No Git repository, `.gitignore`, starter policy, deployment folder or README
is created by default. Use `pf init --scaffold` when those extras are wanted.
Legacy configuration migration still retains its audit under `.pf/migration.json`.
Nothing is published or deployed to a cluster; the final status leaves those
stages pending. A verified image is not a vulnerability scan.

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
- init creates only `pf.yaml` and `pf.lock` beside the source; optional Git
  initialization, starter directories and inspection files require `--scaffold`;
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

## Verify binary packaging without a container engine

From the repository root:

```bash
./demo/verify-binary-packaging.sh
```

Prerequisites: the Go version pinned in `go.mod`, Python 3 and enough disk space
for temporary build outputs. Building the tools may download Go modules on a
fresh machine. No Docker/Podman engine, registry credentials or cluster is used.
The script does not install binaries globally, start a workload or publish an
artifact. Its temporary directory is printed and retained for inspection.

The script builds a Linux/amd64 sample executable, packages that same input
twice, verifies both OCI layouts and compares their manifest digests. It then
verifies an untouched copy, appends JSON whitespace to its manifest blob, and
requires the changed artifact to be refused. This demonstrates deterministic
packaging and artifact integrity for this fixture. It is not a benchmark,
a claim that arbitrary source builds are reproducible, or a full signing demo.
Native backend execution is not exercised by this engine-free proof.
