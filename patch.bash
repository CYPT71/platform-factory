#!/usr/bin/env bash
set -Eeuo pipefail

# Local equivalent of .github/workflows/ci-microvm.yml
#
# Usage:
#   ./ci-microvm-local.sh all
#   ./ci-microvm-local.sh kvm
#   ./ci-microvm-local.sh hvf
#   ./ci-microvm-local.sh sign
#
# Environment overrides:
#   GOTOOLCHAIN_VERSION=1.25.12
#   CACHE_DIR=.cache/microvm
#   ARTIFACT_DIR=.artifacts/ci-microvm
#   SKIP_APT=1
#   SKIP_KERNEL_BUILD=1
#   SKIP_HARDENING_CHECKER=1
#   SKIP_COSIGN=1
#   KEEP_TMP=1
#
# Notes:
# - GitHub-only actions (checkout, actions/cache, upload-artifact, OIDC)
#   are represented locally by validation, persistent cache directories,
#   artifact staging, and optional local cosign signing.
# - Run the KVM path on Ubuntu 24.04 with /dev/kvm.
# - Run the HVF path on macOS 15+ with Apple Silicon.

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

MODE="${1:-all}"
GOTOOLCHAIN_VERSION="${GOTOOLCHAIN_VERSION:-1.25.12}"
CACHE_DIR="${CACHE_DIR:-$ROOT/.cache/microvm}"
ARTIFACT_DIR="${ARTIFACT_DIR:-$ROOT/.artifacts/ci-microvm}"
TMP_DIR="${TMP_DIR:-$(mktemp -d -t platform-factory-microvm.XXXXXX)}"
KVM_CACHE="$CACHE_DIR/amd64"
HVF_CACHE="$CACHE_DIR/arm64"

LOG_DIR="$ARTIFACT_DIR/logs"
EVIDENCE_DIR="$ARTIFACT_DIR/evidence"
SIGN_DIR="$ARTIFACT_DIR/signed"

mkdir -p "$CACHE_DIR" "$LOG_DIR" "$EVIDENCE_DIR" "$SIGN_DIR"

cleanup() {
  if [[ "${KEEP_TMP:-0}" != "1" ]]; then
    rm -rf "$TMP_DIR"
  else
    printf 'Temporary files retained at: %s\n' "$TMP_DIR"
  fi
}
trap cleanup EXIT

log() {
  printf '\n[%s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"
}

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

run_logged() {
  local name="$1"
  shift
  log "$name"
  "$@" 2>&1 | tee "$LOG_DIR/${name// /-}.log"
  local status=${PIPESTATUS[0]}
  (( status == 0 )) || return "$status"
}

require_file() {
  [[ -f "$1" ]] || die "Missing required file: $1"
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "Missing required command: $1"
}

verify_repo() {
  log "Validate repository checkout"
  require_file "$ROOT/go.mod"
  require_file "$ROOT/scripts/microvm/build-kernel.sh"
  require_file "$ROOT/scripts/microvm/test-podman-kvm.sh"
  require_file "$ROOT/scripts/microvm/test-hvf-local.sh"
  require_file "$ROOT/scripts/ci/write-traceability.py"
  require_file "$ROOT/scripts/ci/write-microvm-evidence-bundle.py"
  git rev-parse --is-inside-work-tree >/dev/null
  git status --short
}

verify_go() {
  require_cmd go
  local actual
  actual="$(go version)"
  printf 'Detected: %s\n' "$actual"
  if [[ "$actual" != *"go${GOTOOLCHAIN_VERSION}"* ]]; then
    printf 'WARNING: workflow pins Go %s, current toolchain differs.\n' "$GOTOOLCHAIN_VERSION" >&2
  fi
}

install_linux_dependencies() {
  [[ "$(uname -s)" == "Linux" ]] || return 0
  if [[ "${SKIP_APT:-0}" == "1" ]]; then
    log "Skip apt dependency installation"
    return 0
  fi

  require_cmd sudo
  run_logged "apt-update" sudo apt-get update
  run_logged "apt-install" sudo apt-get install -y --no-install-recommends \
    qemu-system-x86 \
    cpio \
    build-essential \
    flex \
    bison \
    bc \
    libssl-dev \
    libelf-dev \
    podman \
    python3 \
    python3-pip \
    git \
    curl \
    ca-certificates
}

check_kvm() {
  [[ "$(uname -s)" == "Linux" ]] || die "KVM job requires Linux"
  [[ -e /dev/kvm ]] || die "/dev/kvm is unavailable"
  ls -la /dev/kvm
  grep -Em1 'vmx|svm' /proc/cpuinfo >/dev/null || die "CPU virtualization flags not found"

  if [[ ! -r /dev/kvm || ! -w /dev/kvm ]]; then
    require_cmd sudo
    sudo chown "$(id -u):$(id -g)" /dev/kvm
    sudo chmod 600 /dev/kvm
  fi

  run_logged "check-kvm" "$ROOT/scripts/microvm/check-kvm.sh"
  run_logged "kvm-flat-payload-test" \
    env GOTOOLCHAIN=local \
    go test ./internal/vmm -run '^TestRunFlatPayloadBootsAndHalts$' -count=1
}

build_oci_image_amd64() {
  log "Build cmd/example-service into AMD64 OCI image"
  rm -rf "$TMP_DIR/oci-image"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath \
    -ldflags='-s -w -X main.debugExitEnabled=true' \
    -o "$TMP_DIR/example-service" ./cmd/example-service

  go run ./cmd/oci-builder \
    -binary "$TMP_DIR/example-service" \
    -output "$TMP_DIR/oci-image" \
    -arch amd64

  test -f "$TMP_DIR/oci-image/oci-layout"
  test -f "$TMP_DIR/oci-image/index.json"
}

build_kvm_kernel() {
  mkdir -p "$KVM_CACHE"
  if [[ "${SKIP_KERNEL_BUILD:-0}" == "1" && -s "$KVM_CACHE/kernel" ]]; then
    log "Reuse cached AMD64 kernel"
  else
    run_logged "kernel-build-amd64" \
      timeout --signal=TERM 25m \
      "$ROOT/scripts/microvm/build-kernel.sh" amd64 "$KVM_CACHE/kernel"
  fi

  test -s "$KVM_CACHE/kernel"
  test -s "$KVM_CACHE/kernel.provenance.json"
  test -s "$KVM_CACHE/kernel.sbom.cdx.json"
  test -s "$KVM_CACHE/kernel.config.resolved"

  local forbidden
  for forbidden in \
    DEVMEM DEVPORT KEXEC KEXEC_FILE HIBERNATION CRASH_DUMP \
    FB LEGACY_PTYS LEGACY_TIOCSTI BPF_SYSCALL IA32_EMULATION \
    X86_X32_ABI MAGIC_SYSRQ
  do
    if grep -Eq "^CONFIG_${forbidden}=[ym]$" "$KVM_CACHE/kernel.config.resolved"; then
      die "kernel hardening regression: CONFIG_${forbidden} is enabled"
    fi
  done

  local required
  for required in \
    PRINTK BINFMT_ELF FUTEX EPOLL EVENTFD SIGNALFD \
    TIMERFD POSIX_TIMERS MULTIUSER TTY DEVTMPFS DEVTMPFS_MOUNT \
    PROC_FS SYSFS BLK_DEV_INITRD RD_GZIP VIRTIO_CONSOLE
  do
    grep -qx "CONFIG_${required}=y" "$KVM_CACHE/kernel.config.resolved" \
      || die "kernel runtime regression: CONFIG_${required} is not built in"
  done

  grep -qx 'CONFIG_SYN_COOKIES=y' "$KVM_CACHE/kernel.config.resolved"
  grep -qx 'CONFIG_POSIX_MQUEUE=y' "$KVM_CACHE/kernel.config.resolved"
  grep -qx 'CONFIG_POSIX_MQUEUE_SYSCTL=y' "$KVM_CACHE/kernel.config.resolved"
  grep -qx 'CONFIG_SERIAL_8250_NR_UARTS=2' "$KVM_CACHE/kernel.config.resolved"
  grep -qx 'CONFIG_SERIAL_8250_RUNTIME_UARTS=2' "$KVM_CACHE/kernel.config.resolved"
}

build_native_kvm_guest() {
  log "Build native KVM guest initramfs"

  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags='-s -w' \
    -o "$TMP_DIR/native-microvm-init" ./cmd/microvm-init

  go build -trimpath -ldflags='-s -w' \
    -o "$TMP_DIR/microvm-initramfs" ./cmd/microvm-initramfs

  "$TMP_DIR/microvm-initramfs" \
    -layout "$TMP_DIR/oci-image" \
    -init "$TMP_DIR/native-microvm-init" \
    -output "$TMP_DIR/native-initramfs.cpio.gz" \
    | tee "$LOG_DIR/native-initramfs-assembly.json"

  test -s "$TMP_DIR/native-initramfs.cpio.gz"
}

test_native_kvm_boot() {
  run_logged "native-kvm-linux-boot" \
    env \
      GOTOOLCHAIN=local \
      SECURE_OCI_TEST_BZIMAGE="$KVM_CACHE/kernel" \
      SECURE_OCI_TEST_INITRD="$TMP_DIR/native-initramfs.cpio.gz" \
    go test ./internal/vmm -run '^TestRunLinuxWithRealKVM$' -count=1 -v
}

test_podman_kvm() {
  log "Prove Podman owns the native-KVM MicroVM lifecycle"
  (
    cd "$ROOT"
    GOTOOLCHAIN=local \
    SECURE_OCI_EVIDENCE_DIR="$EVIDENCE_DIR" \
    scripts/microvm/test-podman-kvm.sh \
      "$TMP_DIR/oci-image" \
      platform-factory:latest \
      "$KVM_CACHE/kernel" \
      "$TMP_DIR/native-microvm-init"
  ) 2>&1 | tee "$LOG_DIR/podman-kvm.log"
  local status=${PIPESTATUS[0]}
  (( status == 0 )) || return "$status"
}

install_hardening_checker() {
  if [[ "${SKIP_HARDENING_CHECKER:-0}" == "1" ]]; then
    log "Skip kernel-hardening-checker"
    return 0
  fi

  run_logged "install-kernel-hardening-checker" \
    sudo python3 -m pip install --break-system-packages --quiet \
    "git+https://github.com/a13xp0p0v/kernel-hardening-checker@afc376f2a935994793343cfeb05953583cc30191"
}

scan_kernel_hardening() {
  if [[ "${SKIP_HARDENING_CHECKER:-0}" == "1" ]]; then
    return 0
  fi

  log "Kernel hardening report"
  set +e
  kernel-hardening-checker \
    -c "$KVM_CACHE/kernel.config.resolved" \
    -m verbose \
    | tee "$EVIDENCE_DIR/kernel-hardening-report.txt"
  local verbose_status=${PIPESTATUS[0]}

  kernel-hardening-checker \
    -c "$KVM_CACHE/kernel.config.resolved" \
    -m json \
    > "$EVIDENCE_DIR/kernel-hardening-report.json"
  local json_status=$?
  set -e

  printf 'kernel-hardening-checker verbose exit status: %s\n' "$verbose_status"
  printf 'kernel-hardening-checker JSON exit status: %s\n' "$json_status"

  test -s "$EVIDENCE_DIR/kernel-hardening-report.txt"
  test -s "$EVIDENCE_DIR/kernel-hardening-report.json"
}

verify_initramfs_reproducibility() {
  log "Verify native initramfs byte reproducibility"

  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags='-s -w' \
    -o "$TMP_DIR/repro-microvm-init" ./cmd/microvm-init

  go build -trimpath -ldflags='-s -w' \
    -o "$TMP_DIR/repro-microvm-initramfs" ./cmd/microvm-initramfs

  "$TMP_DIR/repro-microvm-initramfs" \
    -layout "$TMP_DIR/oci-image" \
    -init "$TMP_DIR/repro-microvm-init" \
    -output "$TMP_DIR/initramfs-a.cpio.gz"

  sleep 2

  "$TMP_DIR/repro-microvm-initramfs" \
    -layout "$TMP_DIR/oci-image" \
    -init "$TMP_DIR/repro-microvm-init" \
    -output "$TMP_DIR/initramfs-b.cpio.gz"

  sha256sum \
    "$TMP_DIR/initramfs-a.cpio.gz" \
    "$TMP_DIR/initramfs-b.cpio.gz" \
    | tee "$EVIDENCE_DIR/initramfs-reproducibility.txt"

  cmp "$TMP_DIR/initramfs-a.cpio.gz" "$TMP_DIR/initramfs-b.cpio.gz"
}

boot_qemu_smoke_tests() {
  log "Boot image under KVM and smoke-test it"
  (
    export GOTOOLCHAIN=local
    export MICROVM_KERNEL="$KVM_CACHE/kernel"
    export MICROVM_SMOKE_TEST_PATH=/healthz
    export MICROVM_BOOT_MANIFEST="$EVIDENCE_DIR/boot-manifest.json"
    timeout --signal=TERM 5m scripts/microvm/run-microvm.sh "$TMP_DIR/oci-image"
  ) 2>&1 | tee "$LOG_DIR/microvm-boot.txt"
  local status=${PIPESTATUS[0]}
  (( status == 0 )) || return "$status"
  test -s "$EVIDENCE_DIR/boot-manifest.json"

  log "Boot image and verify guest graceful shutdown"
  (
    export GOTOOLCHAIN=local
    export MICROVM_KERNEL="$KVM_CACHE/kernel"
    export MICROVM_SHUTDOWN_TEST_PATH=/debug/exit
    timeout --signal=TERM 5m scripts/microvm/run-microvm.sh "$TMP_DIR/oci-image"
  ) 2>&1 | tee "$LOG_DIR/microvm-shutdown.txt"
  status=${PIPESTATUS[0]}
  (( status == 0 )) || return "$status"
}

stage_kernel_evidence() {
  log "Stage kernel evidence"
  mkdir -p "$EVIDENCE_DIR/kernel-evidence"
  cp "$KVM_CACHE/kernel.config.resolved" "$EVIDENCE_DIR/kernel-evidence/"
  cp "$KVM_CACHE/kernel.provenance.json" "$EVIDENCE_DIR/kernel-evidence/"
  cp "$KVM_CACHE/kernel.sbom.cdx.json" "$EVIDENCE_DIR/kernel-evidence/"
  test -s "$EVIDENCE_DIR/kernel-evidence/kernel.config.resolved"
  test -s "$EVIDENCE_DIR/kernel-evidence/kernel.provenance.json"
  test -s "$EVIDENCE_DIR/kernel-evidence/kernel.sbom.cdx.json"
}

write_traceability() {
  log "Write local traceability manifest"

  local files=(
    "$LOG_DIR/kernel-build-amd64.log"
    "$LOG_DIR/native-kvm-linux-boot.log"
    "$LOG_DIR/microvm-boot.txt"
    "$LOG_DIR/microvm-shutdown.txt"
    "$EVIDENCE_DIR/podman-microvm-result.txt"
    "$EVIDENCE_DIR/podman-microvm-ps.json"
    "$EVIDENCE_DIR/podman-microvm-logs.txt"
    "$KVM_CACHE/kernel.config.resolved"
    "$KVM_CACHE/kernel.provenance.json"
    "$KVM_CACHE/kernel.sbom.cdx.json"
    "$EVIDENCE_DIR/kernel-hardening-report.json"
    "$EVIDENCE_DIR/initramfs-reproducibility.txt"
    "$EVIDENCE_DIR/boot-manifest.json"
    "$LOG_DIR/native-initramfs-assembly.json"
  )

  local existing=()
  local f
  for f in "${files[@]}"; do
    [[ -f "$f" ]] && existing+=("$f")
  done

  python3 scripts/ci/write-traceability.py \
    "$EVIDENCE_DIR/traceability.json" \
    "${existing[@]}"
}

prepare_hvf_guest() {
  [[ "$(uname -s)" == "Darwin" ]] || die "HVF job requires macOS"
  [[ "$(uname -m)" == "arm64" ]] || die "HVF job requires Apple Silicon arm64"

  require_cmd go
  mkdir -p "$HVF_CACHE"

  if [[ ! -s "$HVF_CACHE/kernel" || "${SKIP_KERNEL_BUILD:-0}" != "1" ]]; then
    log "Build ARM64 HVF kernel"
    MICROVM_CROSS_COMPILE=1 \
    CROSS_COMPILE=aarch64-linux-gnu- \
      scripts/microvm/build-kernel.sh arm64 "$HVF_CACHE/kernel"
  fi

  test -s "$HVF_CACHE/kernel"
  grep -qx 'CONFIG_VIRTIO_CONSOLE=y' "$HVF_CACHE/kernel.config.resolved"
  grep -qx 'CONFIG_POSIX_MQUEUE=y' "$HVF_CACHE/kernel.config.resolved"
  grep -qx 'CONFIG_POSIX_MQUEUE_SYSCTL=y' "$HVF_CACHE/kernel.config.resolved"

  if [[ ! -s "$HVF_CACHE/initramfs.cpio.gz" ]]; then
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
      go build -trimpath \
      -ldflags='-s -w -X main.debugExitEnabled=true' \
      -o "$TMP_DIR/example-service-arm64" ./cmd/example-service

    go run ./cmd/oci-builder \
      -binary "$TMP_DIR/example-service-arm64" \
      -output "$TMP_DIR/oci-image-arm64" \
      -arch arm64

    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
      go build -trimpath -ldflags='-s -w' \
      -o "$TMP_DIR/microvm-init-arm64" ./cmd/microvm-init

    go build -trimpath -ldflags='-s -w' \
      -o "$TMP_DIR/microvm-initramfs-host" ./cmd/microvm-initramfs

    "$TMP_DIR/microvm-initramfs-host" \
      -layout "$TMP_DIR/oci-image-arm64" \
      -platform linux/arm64 \
      -init "$TMP_DIR/microvm-init-arm64" \
      -output "$HVF_CACHE/initramfs.cpio.gz"
  fi

  test -s "$HVF_CACHE/initramfs.cpio.gz"
}

boot_hvf() {
  [[ "$(uname -s)" == "Darwin" ]] || die "HVF job requires macOS"

  run_logged "hvf-boot" \
    env \
      GOTOOLCHAIN=local \
      SECURE_OCI_ALLOW_UNAVAILABLE_HVF=1 \
      SECURE_OCI_TEST_KERNEL_IMAGE="$HVF_CACHE/kernel" \
      SECURE_OCI_TEST_INITRD="$HVF_CACHE/initramfs.cpio.gz" \
    scripts/microvm/test-hvf-local.sh
}

sign_evidence() {
  log "Build signable evidence bundle"

  require_file "$EVIDENCE_DIR/kernel-evidence/kernel.provenance.json"
  require_file "$EVIDENCE_DIR/kernel-evidence/kernel.sbom.cdx.json"
  require_file "$EVIDENCE_DIR/kernel-hardening-report.json"
  require_file "$EVIDENCE_DIR/boot-manifest.json"

  python3 scripts/ci/write-microvm-evidence-bundle.py \
    "$EVIDENCE_DIR/kernel-evidence/kernel.provenance.json" \
    "$EVIDENCE_DIR/kernel-evidence/kernel.sbom.cdx.json" \
    "$EVIDENCE_DIR/kernel-hardening-report.json" \
    "$EVIDENCE_DIR/boot-manifest.json" \
    "$SIGN_DIR/microvm-evidence-bundle.json"

  if [[ "${SKIP_COSIGN:-0}" == "1" ]]; then
    log "Skip cosign signing"
    return 0
  fi

  require_cmd cosign

  # GitHub Actions uses keyless OIDC signing. Locally, cosign may open an
  # interactive browser flow depending on its configuration.
  cosign sign-blob --yes \
    --bundle "$SIGN_DIR/microvm-evidence-bundle.cosign.bundle" \
    "$SIGN_DIR/microvm-evidence-bundle.json"

  cosign verify-blob \
    --bundle "$SIGN_DIR/microvm-evidence-bundle.cosign.bundle" \
    "$SIGN_DIR/microvm-evidence-bundle.json" \
    | tee "$SIGN_DIR/microvm-evidence-verification.txt"
}

run_kvm_job() {
  verify_repo
  verify_go
  install_linux_dependencies
  check_kvm
  build_oci_image_amd64
  build_kvm_kernel
  build_native_kvm_guest
  test_native_kvm_boot
  test_podman_kvm
  install_hardening_checker
  scan_kernel_hardening
  verify_initramfs_reproducibility
  boot_qemu_smoke_tests
  stage_kernel_evidence
  write_traceability
}

run_hvf_job() {
  verify_repo
  verify_go
  prepare_hvf_guest
  boot_hvf
}

case "$MODE" in
  all)
    if [[ "$(uname -s)" == "Linux" ]]; then
      run_kvm_job
      if [[ "${SKIP_COSIGN:-0}" != "1" ]]; then
        sign_evidence
      fi
    elif [[ "$(uname -s)" == "Darwin" ]]; then
      run_hvf_job
    else
      die "Unsupported operating system: $(uname -s)"
    fi
    ;;
  kvm)
    run_kvm_job
    ;;
  hvf)
    run_hvf_job
    ;;
  sign)
    verify_repo
    sign_evidence
    ;;
  *)
    cat >&2 <<EOF
Usage: $0 {all|kvm|hvf|sign}

  all   Run the platform-appropriate workflow
  kvm   Run the Ubuntu/KVM workflow
  hvf   Run the macOS/HVF workflow
  sign  Build and sign the evidence bundle
EOF
    exit 2
    ;;
esac

log "Completed successfully"
printf 'Artifacts: %s\n' "$ARTIFACT_DIR"
