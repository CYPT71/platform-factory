# Platform Factory MVP demo

This demo proves the smallest complete user outcome from a clean project:

```text
main.go → pf init → verified OCI layout → Docker/Podman → hello from pf mvp
```

It builds the real CLI and the real Go language plugin from this checkout. The
application workspace itself starts with only `hello-world/main.go`; no
pre-generated `pf.yaml`, lock file, container file, or OCI layout is used.

## Recorded terminal demo

The [README animation](../docs/assets/from-zero.gif) records a real
[FastAPI Hello World](https://fastapi.tiangolo.com/tutorial/first-steps/): source
creation, dependency freezing, OCI build and verification, an HTTP request, and
**`podman ps` as the final command**, showing the server still running.
Commands and TUI choices were entered individually in an interactive Bash
terminal from an empty application folder. No demo script executed the sequence
and no output was substituted. Only idle pauses were shortened in the GIF;
the [original recording](../docs/assets/from-zero.cast) retains output and timing.

Prerequisites: PF and its Python plugin, a `python` command with pip, curl, and
a running Podman engine. Installation and Podman startup happen before recording.
The target is Linux/amd64 with Python 3.12; the recorded Mac ARM host uses Podman
AMD64 execution support. Use a unique project directory and an available local
port 8000. The name `pf-fastapi-demo` must also be unused.

From the empty directory, enter these commands individually:

```bash
ls -A
cat > app.py <<'PY'
from fastapi import FastAPI
import uvicorn

app = FastAPI()

@app.get("/")
def hello():
    return {"message": "Hello World"}

if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=8000)
PY
cat > requirements.txt <<'REQ'
fastapi==0.115.12
uvicorn==0.34.2
pydantic==1.10.22
REQ
export PIP_PLATFORM=any PIP_PYTHON_VERSION=3.12 PIP_ONLY_BINARY=:all:
clear
pf init --engine podman .
```

These versions define the tested fixture, rather than a recommendation of the
latest releases. The pip options select Python 3.12-compatible, pure-Python
wheels, including Pydantic 1's pure-Python distribution. Native extensions with
additional Linux library requirements are outside this fixture's scope.

Review the plan: PF adds only `pf.yaml` and `pf.lock`. Confirm with `y`.
In the runtime TUI, select **Pull a base image**, then enter:

```text
python@sha256:05a994d2344eb364cc186910f4fc8786a38215f39f00d0acc25bff33a08f4177
```

This explicitly downloads and stages the pinned interpreter under
`.platform-factory/`. The slim base lacks Tk libraries; the plugin reports and
excludes the unusable `_tkinter` extension. Then continue:

```bash
clear
pf freeze 2>/tmp/pf-fastapi-freeze.log
pf project build --yes 2>/tmp/pf-fastapi-build.log
clear
pf verify --format text .platform-factory/image
nohup pf run --runtime podman --name pf-fastapi-demo --network bridge -p 127.0.0.1:8000:8000 --env PYTHONPATH=/app/.platform-factory/deps/python .platform-factory/image >/tmp/pf-fastapi-server.log 2>&1 &
```

`pf freeze` downloads dependencies explicitly and records the resolved versions
in `requirements.lock`. The build consumes those frozen inputs. The runtime
`PYTHONPATH` points at the packaged dependency directory; it does not load host
packages. The foreground PF runner is placed in the background with `nohup`,
while its container retains PF's normal execution restrictions. Only the local
loopback port is exposed. Detailed diagnostics remain in the displayed `/tmp`
log files rather than adding logs to the application folder.

Once the server is ready, finish exactly as the recording does:

```bash
clear
curl -fsSi -w '\n' http://127.0.0.1:8000/
podman ps --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'
```

The response is HTTP 200 with `{"message":"Hello World"}`. The last table shows
`pf-fastapi-demo`, its running status, and `127.0.0.1:8000->8000/tcp`.
Source, requirements, dependency lock and PF config/lock remain at the root;
generated runtime, dependency, freeze/build and OCI data share
`.platform-factory/`. No Git repository, `.gitignore`, starter folders or README
is created by default. Use `pf init --scaffold` for those optional extras.
Legacy configuration migration retains its audit under `.pf/migration.json`.

After your presentation, stop the demonstration container with
`podman stop pf-fastapi-demo`. Its `--rm` lifecycle removes the stopped container.
The recording ends before cleanup, while the service is running. No registry
publication or cluster deployment is performed, and image verification is not
a vulnerability scan.

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
