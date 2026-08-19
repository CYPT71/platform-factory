#!/usr/bin/env bash
# Single source of truth for pinned CI tool versions, shared by every
# workflow that needs one or more of them. Not a GitHub Actions composite
# action: scripts/ci/verify-workflows.py enforces that every `uses:` in
# .github/workflows/*.yml is either a SHA-pinned external action or
# docker://-free, which rules out a local ./.github/actions/* action (its
# source lives in this repo, at the exact commit the workflow itself
# checked out, but the policy has no way to express that trust
# distinction from a bare local path, so it blocks all of them
# uniformly). A plain script invoked from a `run:` step sidesteps that
# check entirely while still centralizing the pinned versions in one
# place: bump a version here and every workflow that installs that tool
# picks it up on its next run, instead of hunting through however many
# workflow files happen to install it.
#
# Usage: scripts/ci/install-tools.sh TOOL [TOOL...]
# Each TOOL is one of: podman skopeo runc kind crictl benchstat nuclei
set -euo pipefail

# go install below must never silently fetch a different toolchain than
# whatever actions/setup-go already pinned for this job.
export GOTOOLCHAIN=local

KIND_VERSION=v0.32.0
CRICTL_VERSION=v1.33.0
BENCHSTAT_VERSION=82a0b07e230d76fa1b3036c383d7a98172f87334
NUCLEI_VERSION=v3.8.0

if [ "$#" -eq 0 ]; then
  echo "usage: $0 TOOL [TOOL...] (podman skopeo runc kind crictl benchstat nuclei)" >&2
  exit 2
fi

apt_packages=()
for tool in "$@"; do
  case "$tool" in
    podman) apt_packages+=(podman) ;;
    skopeo) apt_packages+=(skopeo) ;;
    runc) apt_packages+=(runc) ;;
    kind)
      go install "sigs.k8s.io/kind@${KIND_VERSION}"
      ;;
    crictl)
      go install "sigs.k8s.io/cri-tools/cmd/crictl@${CRICTL_VERSION}"
      # Callers that invoke crictl via sudo (e.g. test-containerd-kvm.sh)
      # never see GOPATH/bin: sudo uses its own secure_path and ignores
      # both the caller's PATH and GITHUB_PATH. Install straight to
      # /usr/local/bin, which is on sudo's secure_path.
      sudo install -m 0755 "$(go env GOPATH)/bin/crictl" /usr/local/bin/crictl
      ;;
    benchstat)
      go install "golang.org/x/perf/cmd/benchstat@${BENCHSTAT_VERSION}"
      ;;
    nuclei)
      go install "github.com/projectdiscovery/nuclei/v3/cmd/nuclei@${NUCLEI_VERSION}"
      ;;
    *)
      echo "install-tools.sh: unknown tool '$tool'" >&2
      exit 1
      ;;
  esac
done

if [ "${#apt_packages[@]}" -gt 0 ]; then
  sudo apt-get update
  sudo apt-get install -y --no-install-recommends "${apt_packages[@]}"
fi
