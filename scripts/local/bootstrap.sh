#!/usr/bin/env bash
# Build every Go command into an isolated local environment, or install the
# resulting binaries into an explicit prefix. Supports Linux, macOS and
# Windows targets from a POSIX host.
set -euo pipefail

usage() {
  cat <<'EOF'
usage: scripts/local/bootstrap.sh [OPTIONS]

Options:
  --target OS/ARCH    linux|darwin|windows and amd64|arm64 (default: host)
  --env DIR           isolated environment directory (default: .platform-factory-env)
  --install PREFIX    also copy commands into PREFIX/bin
  --clean             replace an existing environment
  --rosetta        skip Rosetta 2 verification of darwin/amd64 binaries
                       when cross-building from an Apple Silicon host
  -h, --help          show this help

Examples:
  scripts/local/bootstrap.sh
  source .platform-factory-env/activate
  scripts/local/bootstrap.sh --target windows/amd64 --env dist/windows
  scripts/local/bootstrap.sh --install "$HOME/.local"
EOF
}

if [ -n "${BASH_VERSION:-}" ]; then
  script_path=${BASH_SOURCE[0]}
elif [ -n "${ZSH_VERSION:-}" ]; then
  script_path=${(%):-%x}
else
  echo "error: this script requires Bash or Zsh" >&2
  exit 1
fi

repo_root=$(cd "$(dirname "$script_path")/../.." && pwd)
command -v go >/dev/null 2>&1 || { echo "error: Go is required on PATH" >&2; exit 1; }
target="$(go env GOOS)/$(go env GOARCH)"
host_goos=$(go env GOHOSTOS)
host_goarch=$(go env GOHOSTARCH)
environment="$repo_root/.platform-factory-env"
install_prefix=
clean=false
rosetta_verify=false

while [ "$#" -gt 0 ]; do
  case "$1" in
    --target)
      [ "$#" -ge 2 ] || { echo "error: --target requires OS/ARCH" >&2; exit 2; }
      target=$2
      shift 2
      ;;
    --env)
      [ "$#" -ge 2 ] || { echo "error: --env requires a directory" >&2; exit 2; }
      environment=$2
      shift 2
      ;;
    --install)
      [ "$#" -ge 2 ] || { echo "error: --install requires a prefix" >&2; exit 2; }
      install_prefix=$2
      shift 2
      ;;
    --clean)
      clean=true
      shift
      ;;
    --rosetta)
      rosetta_verify=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "error: unknown option '$1'" >&2
      usage >&2
      exit 2
      ;;
  esac
done

goos=${target%/*}
goarch=${target#*/}
if [ "$goos" = "$target" ]; then
  echo "error: target must use OS/ARCH syntax" >&2
  exit 2
fi
case "$goos" in linux|darwin|windows) ;; *) echo "error: unsupported OS '$goos'" >&2; exit 2 ;; esac
case "$goarch" in amd64|arm64) ;; *) echo "error: unsupported architecture '$goarch'" >&2; exit 2 ;; esac

case "$environment" in
  ""|/|"$repo_root")
    echo "error: refusing unsafe environment directory '$environment'" >&2
    exit 2
    ;;
esac
if [ -e "$environment" ] && [ "$clean" != true ]; then
  echo "error: environment already exists: $environment (use --clean to replace it)" >&2
  exit 1
fi
if [ -e "$environment" ]; then
  rm -rf -- "$environment"
fi
mkdir -p "$environment/bin"
environment=$(cd "$environment" && pwd)

suffix=
[ "$goos" = windows ] && suffix=.exe
version=$(git -C "$repo_root" describe --tags --always --dirty 2>/dev/null || echo dev)

native_vmm=false
if [ "$goos" = darwin ]; then
  if [ "$host_goos/$host_goarch" = "$goos/$goarch" ]; then
    native_vmm=true
    echo "native macOS build: enabling CGO for Virtualization.framework support" >&2
  else
    echo "warning: cross-building $goos/$goarch from $host_goos/$host_goarch; the resulting platform-factory binary does not include the native macOS VMM" >&2
  fi
fi

commands="platform-factory oci-builder example-service microvm-init microvm-initramfs platform-factory-control-plane platform-factory-worker"
if [ "$goos/$goarch" = linux/amd64 ]; then
  commands="$commands platform-factory-runtime"
else
  echo "platform-factory-runtime skipped: OCI runtime integration requires linux/amd64 (target is $goos/$goarch)" >&2
fi
language_plugins="go python node java dotnet rust ruby php"
for command_name in $commands; do
  echo "building $command_name for $goos/$goarch..." >&2
  ldflags="-s -w"
  [ "$command_name" = platform-factory ] && ldflags="$ldflags -X main.version=$version"
  command_cgo=0
  if [ "$command_name" = platform-factory ] && [ "$native_vmm" = true ]; then
    command_cgo=1
  fi
  CGO_ENABLED="$command_cgo" GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags="$ldflags" \
    -o "$environment/bin/$command_name$suffix" "$repo_root/cmd/$command_name"
done

for language in $language_plugins; do
  plugin_name="platform-factory-lang-$language"
  echo "building $plugin_name for $goos/$goarch..." >&2
  (
    cd "$repo_root/plugins/lang-$language"
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build -trimpath -ldflags="-s -w" \
      -o "$environment/bin/$plugin_name$suffix" .
  )
done

# A successful cross-build only proves `go build` accepted the target
# triple - it says nothing about whether the result can actually launch
# (a stray CGO/framework dependency invalid for the target arch would
# fail here, not at build time). darwin/amd64 on an Apple Silicon host is
# the one target this repo can actually execute and verify locally,
# through Rosetta 2 - so do that, on everything with a plain,
# side-effect-free entry point: the two commands respond to `-h`, and
# every language plugin prints its usage and exits on a bare invocation
# with no args (sdk/langplugin.Dispatch / plugins/lang-go's own check).
# Every other command here is a guest-only init process, a long-running
# daemon, or an HTTP server not meant for direct invocation on the host.
# Skippable with --rosetta for callers who don't want the extra
# per-binary launch cost (e.g. a fast inner dev loop).
if [ "$rosetta_verify" = true ] && [ "$goos/$goarch" = darwin/amd64 ] && [ "$host_goos/$host_goarch" = darwin/arm64 ]; then
  if ! arch -x86_64 /usr/bin/true >/dev/null 2>&1; then
    echo "error: Rosetta 2 is required to verify darwin/amd64 binaries on this Apple Silicon host; install it with: softwareupdate --install-rosetta --agree-to-license" >&2
    exit 1
  fi
  for command_name in platform-factory oci-builder; do
    echo "verifying $command_name runs under Rosetta 2 (darwin/amd64 on $host_goos/$host_goarch)..." >&2
    output=$(arch -x86_64 "$environment/bin/$command_name" -h 2>&1) || true
    if [ -z "$output" ]; then
      echo "error: $command_name (darwin/amd64) produced no output under Rosetta 2 - it did not actually run" >&2
      exit 1
    fi
  done
  for language in $language_plugins; do
    plugin_name="platform-factory-lang-$language"
    echo "verifying $plugin_name runs under Rosetta 2 (darwin/amd64 on $host_goos/$host_goarch)..." >&2
    output=$(arch -x86_64 "$environment/bin/$plugin_name" 2>&1) || true
    if [ -z "$output" ]; then
      echo "error: $plugin_name (darwin/amd64) produced no output under Rosetta 2 - it did not actually run" >&2
      exit 1
    fi
  done
fi

cat >"$environment/activate" <<'EOF'
# Source this file from Bash or Zsh.
if [ -n "${BASH_SOURCE:-}" ]; then
  _platform_factory_source=${BASH_SOURCE[0]}
else
  _platform_factory_source=$0
fi
PLATFORM_FACTORY_ENV=$(CDPATH= cd -- "$(dirname -- "$_platform_factory_source")" && pwd)
unset _platform_factory_source
if [ -z "${PLATFORM_FACTORY_OLD_PATH+x}" ]; then
  export PLATFORM_FACTORY_OLD_PATH="$PATH"
fi
export PLATFORM_FACTORY_ENV
export PATH="$PLATFORM_FACTORY_ENV/bin:$PATH"
deactivate_platform_factory() {
  if [ -n "${PLATFORM_FACTORY_OLD_PATH+x}" ]; then
    export PATH="$PLATFORM_FACTORY_OLD_PATH"
    unset PLATFORM_FACTORY_OLD_PATH
  fi
  unset PLATFORM_FACTORY_ENV
  unset -f deactivate_platform_factory
}
EOF

cat >"$environment/Activate.ps1" <<'EOF'
if (-not $env:PLATFORM_FACTORY_OLD_PATH) { $env:PLATFORM_FACTORY_OLD_PATH = $env:PATH }
$env:PLATFORM_FACTORY_ENV = $PSScriptRoot
$env:PATH = (Join-Path $PSScriptRoot 'bin') + [IO.Path]::PathSeparator + $env:PATH
function global:deactivate-platform-factory {
  if ($env:PLATFORM_FACTORY_OLD_PATH) { $env:PATH = $env:PLATFORM_FACTORY_OLD_PATH }
  Remove-Item Env:PLATFORM_FACTORY_OLD_PATH -ErrorAction SilentlyContinue
  Remove-Item Env:PLATFORM_FACTORY_ENV -ErrorAction SilentlyContinue
  Remove-Item Function:deactivate-platform-factory -ErrorAction SilentlyContinue
}
EOF

cat >"$environment/activate.bat" <<'EOF'
@echo off
if not defined PLATFORM_FACTORY_OLD_PATH set "PLATFORM_FACTORY_OLD_PATH=%PATH%"
set "PLATFORM_FACTORY_ENV=%~dp0"
set "PATH=%~dp0bin;%PATH%"
echo platform-factory environment activated
EOF

cat >"$environment/deactivate.bat" <<'EOF'
@echo off
if defined PLATFORM_FACTORY_OLD_PATH set "PATH=%PLATFORM_FACTORY_OLD_PATH%"
set "PLATFORM_FACTORY_OLD_PATH="
set "PLATFORM_FACTORY_ENV="
echo platform-factory environment deactivated
EOF

json_version=${version//\\/\\\\}
json_version=${json_version//\"/\\\"}
json_commands=
for command_name in $commands; do
  [ -n "$json_commands" ] && json_commands="$json_commands,"
  json_commands="$json_commands\"$command_name\""
done
for language in $language_plugins; do
  [ -n "$json_commands" ] && json_commands="$json_commands,"
  json_commands="$json_commands\"platform-factory-lang-$language\""
done
cat >"$environment/environment.json" <<EOF
{"target_os":"$goos","target_arch":"$goarch","version":"$json_version","native_vmm":$native_vmm,"commands":[$json_commands]}
EOF

if [ -n "$install_prefix" ]; then
  install_bin="$install_prefix/bin"
  mkdir -p "$install_bin"
  for command_name in $commands; do
    install -m 0755 "$environment/bin/$command_name$suffix" "$install_bin/$command_name$suffix"
  done
  for language in $language_plugins; do
    plugin_name="platform-factory-lang-$language"
    install -m 0755 "$environment/bin/$plugin_name$suffix" "$install_bin/$plugin_name$suffix"
  done
  echo "installed commands in $install_bin" >&2
fi

echo "environment ready: $environment" >&2
echo "activate with: source \"$environment/activate\"" >&2
