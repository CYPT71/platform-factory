# The official reproducible dev/CI environment for this repo: the exact Go
# toolchain (see ../../tools/go-version) plus the system packages every
# `go build`/`go test`/`go vet` invocation across the main module and every
# separate go.work module (plugins/*, cmd/platform-factory-installer) needs,
# including the ARM64 kernel cross-build toolchain scripts/microvm/build-kernel.sh
# uses (see .github/workflows/ci-microvm.yml, which installs the same
# packages for the same reason).
#
# This image intentionally does not bake the repo source in - it's a
# toolchain, not a snapshot. Mount the checkout at /workspace:
#
#   podman build -t platform-factory-dev containers/dev
#   podman run --rm -it -v "$PWD:/workspace" -w /workspace platform-factory-dev \
#     bash -c "scripts/bootstrap-dev.sh && make verify"
#
# The pinned digest below is the multi-arch manifest-list digest for
# docker.io/library/golang:1.25.12-bookworm (not a single-platform image
# digest - verified via `skopeo inspect --raw`, which resolves correctly on
# both amd64 and arm64 CI runners), captured at the time this file was
# written. Bump both the tag and the digest together when tools/go-version
# changes, never one without the other.
FROM docker.io/library/golang@sha256:908f8ff2ec296df2f349563072c7925775cd28b50361a52ed834a8a37399b9bf

ENV GOTOOLCHAIN=local \
    DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
      bc \
      bison \
      build-essential \
      ca-certificates \
      flex \
      gcc-aarch64-linux-gnu \
      git \
      libelf-dev \
      libssl-dev \
      make \
      python3 \
      xz-utils \
    && rm -rf /var/lib/apt/lists/*

RUN go version | grep -q "go1.25.12" || { echo "error: base image Go version drifted from tools/go-version" >&2; exit 1; }

# Known gap, not introduced by this image: internal/hypervisor/sandbox's
# seccomp syscall table (syscalls_linux.go) hardcodes x86_64 syscall
# numbers with no linux/arm64 equivalent, so `go build ./...` fails on
# linux/arm64 specifically - it is unaffected on linux/amd64 (CI's own
# tested target) and on darwin/any (the file is Linux-only, excluded by
# its own build tag). Running this image with `--platform linux/amd64`
# (works under QEMU emulation even on an Apple Silicon host) sidesteps it
# until that gap is closed as its own, carefully-verified piece of work -
# getting a seccomp allow-list's syscall numbers wrong is a security bug,
# not something to guess at as a side effect of an unrelated change.

WORKDIR /workspace
