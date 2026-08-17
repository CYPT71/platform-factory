#!/usr/bin/env bash
# Interactive end-user installer: builds and installs only the
# platform-factory binaries the caller actually selects, unlike
# bootstrap.sh which always builds the full command set for CI and
# cross-compilation. Companion to the bubbletea-based
# cmd/platform-factory-installer; this variant needs nothing beyond Go
# and a POSIX shell.
set -euo pipefail

usage() {
  cat <<'EOF'
usage: scripts/local/install.sh [OPTIONS]

Options:
  --components LIST   comma-separated component keys to install
                       non-interactively (core is always included); see --list
  --prefix DIR         installation directory (default: $HOME/.local/bin)
  --os GOOS             target OS (default: host)
  --arch GOARCH          target architecture (default: host)
  --yes                skip the confirmation prompt
  --list               print available components and exit
  -h, --help           show this help

With no options, and when attached to a terminal, runs an interactive menu.

Examples:
  scripts/local/install.sh
  scripts/local/install.sh --components builder,microvm --prefix "$HOME/.local/bin" --yes
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

# --- component catalog -------------------------------------------------
component_keys=(core builder microvm distributed)
component_labels=(
  "Core CLI"
  "OCI Builder"
  "MicroVM support"
  "Distributed platform"
)
component_descriptions=(
  "platform-factory (pf alias) + official language plugins: ready for init, launch, build, pipeline, sbom and diff"
  "oci-builder: builds an OCI image standalone"
  "microvm-init + microvm-initramfs; platform-factory-runtime on linux/amd64 only: isolated microVM execution (KVM/HVF)"
  "platform-factory-control-plane + platform-factory-worker: multi-node orchestration"
)
component_binaries=(
  "platform-factory"
  "oci-builder"
  "microvm-init microvm-initramfs platform-factory-runtime"
  "platform-factory-control-plane platform-factory-worker"
)
component_mandatory=(1 0 0 0)

component_index() {
  local key=$1 i
  for i in "${!component_keys[@]}"; do
    [ "${component_keys[$i]}" = "$key" ] && { echo "$i"; return 0; }
  done
  return 1
}

# --- styling -------------------------------------------------------------
use_color=false
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  use_color=true
fi
style() {
  # style CODE TEXT
  if [ "$use_color" = true ]; then
    printf '\033[%sm%s\033[0m' "$1" "$2"
  else
    printf '%s' "$2"
  fi
}
bold() { style "1" "$1"; }
dim() { style "2" "$1"; }
accent() { style "1;35" "$1"; }
green() { style "1;32" "$1"; }
red() { style "1;31" "$1"; }
plain() { printf '%s' "$1"; }
bold_green() { style "1;32" "$1"; }

# box_width LINE... — the length of the longest (plain, unstyled) line.
box_width() {
  local max=0 s
  for s in "$@"; do
    [ ${#s} -gt "$max" ] && max=${#s}
  done
  echo "$max"
}
box_top() { printf '%s\n' "$(accent "╭$(printf '%*s' "$(($1 + 2))" '' | tr ' ' '─')╮")"; }
box_bottom() { printf '%s\n' "$(accent "╰$(printf '%*s' "$(($1 + 2))" '' | tr ' ' '─')╯")"; }
# box_line WIDTH STYLEFN TEXT — width and padding are computed from the
# plain TEXT so ANSI escapes from STYLEFN never throw off the alignment.
box_line() {
  local width=$1 stylefn=$2 text=$3 pad=$(($1 - ${#3}))
  printf '%s %s%*s %s\n' "$(accent "│")" "$($stylefn "$text")" "$pad" "" "$(accent "│")"
}

print_banner() {
  local title="platform-factory installer"
  local subtitle="installs only the binaries you actually need"
  local width
  width=$(box_width "$title" "$subtitle")
  box_top "$width"
  box_line "$width" bold "$title"
  box_line "$width" dim "$subtitle"
  box_bottom "$width"
}

print_component_list() {
  local i mark
  for i in "${!component_keys[@]}"; do
    if [ "${component_mandatory[$i]}" -eq 1 ]; then
      mark="*"
    else
      mark=" "
    fi
    printf '%s %-12s %s\n' "$mark" "${component_keys[$i]}" "${component_descriptions[$i]}"
    printf '    binaries: %s\n' "${component_binaries[$i]}"
  done
  echo
  dim "* always installed"
  echo
}

# --- option parsing --------------------------------------------------
flag_components=""
flag_prefix="${HOME:-.}/.local/bin"
flag_os=$(go env GOHOSTOS)
flag_arch=$(go env GOHOSTARCH)
flag_yes=false
flag_list=false

while [ "$#" -gt 0 ]; do
  case "$1" in
    --components) [ "$#" -ge 2 ] || { echo "error: --components requires a value" >&2; exit 2; }; flag_components=$2; shift 2 ;;
    --prefix) [ "$#" -ge 2 ] || { echo "error: --prefix requires a value" >&2; exit 2; }; flag_prefix=$2; shift 2 ;;
    --os) [ "$#" -ge 2 ] || { echo "error: --os requires a value" >&2; exit 2; }; flag_os=$2; shift 2 ;;
    --arch) [ "$#" -ge 2 ] || { echo "error: --arch requires a value" >&2; exit 2; }; flag_arch=$2; shift 2 ;;
    --yes) flag_yes=true; shift ;;
    --list) flag_list=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown option '$1'" >&2; usage >&2; exit 2 ;;
  esac
done

if [ "$flag_list" = true ]; then
  print_banner
  print_component_list
  exit 0
fi

# --- selection: interactive menu or flags -----------------------------
selected_keys="core"

interactive=false
if [ -t 0 ] && [ -t 1 ] && [ -z "$flag_components" ] && [ "$flag_yes" = false ]; then
  interactive=true
fi

if [ "$interactive" = true ]; then
  print_banner
  echo
  # toggled[i] mirrors component_keys[i]; core is always on and not toggleable.
  toggled=()
  for i in "${!component_keys[@]}"; do
    if [ "${component_mandatory[$i]}" -eq 1 ]; then
      toggled+=(1)
    else
      toggled+=(0)
    fi
  done

  while true; do
    echo "$(bold "Select additional components") $(dim "(core CLI is always installed)")"
    for i in "${!component_keys[@]}"; do
      box="[ ]"
      if [ "${toggled[$i]}" -eq 1 ]; then
        box="[$(green "x")]"
      fi
      note=""
      [ "${component_mandatory[$i]}" -eq 1 ] && note=" $(dim "(mandatory)")"
      printf '  %d) %s %-22s %s%s\n' "$((i + 1))" "$box" "${component_labels[$i]}" "${component_descriptions[$i]}" "$note"
    done
    echo
    printf '%s' "toggle a number, or press enter to continue: "
    read -r reply || reply=""
    if [ -z "$reply" ]; then
      break
    fi
    for token in $reply; do
      case "$token" in
        ''|*[!0-9]*) echo "  $(red "ignored:") '$token' is not a number" ;;
        *)
          idx=$((token - 1))
          if [ "$idx" -ge 0 ] && [ "$idx" -lt "${#component_keys[@]}" ]; then
            if [ "${component_mandatory[$idx]}" -eq 1 ]; then
              echo "  $(dim "${component_keys[$idx]} is mandatory")"
            elif [ "${toggled[$idx]}" -eq 1 ]; then
              toggled[$idx]=0
            else
              toggled[$idx]=1
            fi
          else
            echo "  $(red "ignored:") '$token' is out of range"
          fi
          ;;
      esac
    done
    echo
  done

  selected_keys=""
  for i in "${!component_keys[@]}"; do
    if [ "${toggled[$i]}" -eq 1 ]; then
      selected_keys="$selected_keys ${component_keys[$i]}"
    fi
  done

  echo
  printf '%s [%s]: ' "install directory" "$flag_prefix"
  read -r reply || reply=""
  [ -n "$reply" ] && flag_prefix=$reply
  echo
else
  if [ -z "$flag_components" ] && [ "$flag_yes" = false ]; then
    echo "error: not a terminal: pass --components and --yes to install non-interactively, or --list to see options" >&2
    exit 1
  fi
  if [ "$flag_yes" != true ]; then
    echo "error: non-interactive mode requires --yes to confirm" >&2
    exit 1
  fi
  if [ -n "$flag_components" ]; then
    selected_keys="core ${flag_components//,/ }"
  fi
fi

# de-duplicate and validate
final_keys=""
for key in $selected_keys; do
  if ! component_index "$key" >/dev/null; then
    echo "error: unknown component '$key' (use --list to see available components)" >&2
    exit 1
  fi
  case " $final_keys " in
    *" $key "*) ;;
    *) final_keys="$final_keys $key" ;;
  esac
done

if [ "$interactive" = true ]; then
  echo "$(bold "About to install:")"
  for key in $final_keys; do
    idx=$(component_index "$key")
    echo "  • ${component_labels[$idx]} ($(dim "${component_binaries[$idx]}"))"
  done
  echo "into $(bold "$flag_prefix")"
  echo
  printf 'proceed? [Y/n]: '
  read -r reply || reply=""
  case "$reply" in
    ""|[Yy]*) ;;
    *) echo "installation cancelled"; exit 1 ;;
  esac
  echo
fi

# --- build --------------------------------------------------------------
mkdir -p "$flag_prefix"
# Builds run from individual module directories. Resolve the user-facing
# prefix first so a relative path always remains relative to the directory
# where install.sh was invoked, not to whichever module is being compiled.
flag_prefix=$(cd "$flag_prefix" && pwd)
version=$(git -C "$repo_root" describe --tags --always --dirty 2>/dev/null || echo dev)
suffix=
[ "$flag_os" = windows ] && suffix=.exe

native_vmm=false
host_goos=$(go env GOHOSTOS)
host_goarch=$(go env GOHOSTARCH)
if [ "$flag_os" = darwin ] && [ "$host_goos/$host_goarch" = "$flag_os/$flag_arch" ]; then
  native_vmm=true
fi

spin() {
  # spin PID LABEL — show a spinner while PID runs, then report ok/failed.
  local pid=$1 label=$2 frames='|/-\' i=0
  if [ "$use_color" = true ] && [ -t 1 ]; then
    while kill -0 "$pid" 2>/dev/null; do
      printf '\r  %s %s' "${frames:i++%${#frames}:1}" "$label"
      sleep 0.1
    done
    wait "$pid"
    status=$?
    if [ "$status" -eq 0 ]; then
      printf '\r  %s %s\n' "$(green "✓")" "$label"
    else
      printf '\r  %s %s\n' "$(red "✗")" "$label"
    fi
    return "$status"
  fi
  printf '  building %s... ' "$label"
  wait "$pid"
  status=$?
  [ "$status" -eq 0 ] && echo ok || echo FAILED
  return "$status"
}

build_failed=false
for key in $final_keys; do
  idx=$(component_index "$key")
  for name in ${component_binaries[$idx]}; do
    if [ "$name" = platform-factory-runtime ] && { [ "$flag_os" != linux ] || [ "$flag_arch" != amd64 ]; }; then
      echo "  $(dim "–") platform-factory-runtime skipped: OCI runtime integration requires linux/amd64 (target is $flag_os/$flag_arch)"
      continue
    fi
    out="$flag_prefix/$name$suffix"
    ldflags="-s -w"
    [ "$name" = platform-factory ] && ldflags="$ldflags -X main.version=$version"
    cgo=0
    [ "$name" = platform-factory ] && [ "$native_vmm" = true ] && cgo=1
    log=$(mktemp)
    (
      cd "$repo_root"
      CGO_ENABLED="$cgo" GOOS="$flag_os" GOARCH="$flag_arch" \
        go build -trimpath -ldflags="$ldflags" -o "$out" "./cmd/$name"
    ) >"$log" 2>&1 &
    build_pid=$!
    if ! spin "$build_pid" "$name"; then
      echo "$(red "build failed:") $name" >&2
      cat "$log" >&2
      build_failed=true
    fi
    rm -f "$log"
  done
done

if [ "$build_failed" = true ]; then
  exit 1
fi

# The official language plugins are part of the core product. Keep them next
# to platform-factory so discovery works immediately, without a per-user
# registry, environment variable, or a surprising `pf plugin load` step.
case " $final_keys " in
  *" core "*)
    for language in go python node java dotnet rust ruby php; do
      name="platform-factory-lang-$language"
      out="$flag_prefix/$name$suffix"
      log=$(mktemp)
      (
        cd "$repo_root/plugins/lang-$language"
        CGO_ENABLED=0 GOOS="$flag_os" GOARCH="$flag_arch" \
          go build -trimpath -ldflags="-s -w" -o "$out" .
      ) >"$log" 2>&1 &
      build_pid=$!
      if ! spin "$build_pid" "$name"; then
        echo "$(red "build failed:") $name" >&2
        cat "$log" >&2
        build_failed=true
      fi
      rm -f "$log"
    done
    ;;
esac

if [ "$build_failed" = true ]; then
  exit 1
fi

# pf is a plain alias for platform-factory. Only created if
# platform-factory was actually part of this install. A symlink on POSIX
# (cheap, always in sync with a later rebuild in place, relative so the
# whole install directory stays relocatable); a real file copy on
# Windows, where creating a symlink needs a privilege an ordinary
# install run cannot assume.
platform_factory_out="$flag_prefix/platform-factory$suffix"
if [ -e "$platform_factory_out" ]; then
  pf_out="$flag_prefix/pf$suffix"
  rm -f "$pf_out"
  if [ "$flag_os" = windows ]; then
    cp "$platform_factory_out" "$pf_out"
  else
    ln -s "platform-factory$suffix" "$pf_out"
  fi
fi

# --- summary --------------------------------------------------------------
echo
summary_line="installed into $flag_prefix"
summary_width=$(box_width "Installation complete" "$summary_line")
box_top "$summary_width"
box_line "$summary_width" bold_green "Installation complete"
box_line "$summary_width" plain "$summary_line"
box_bottom "$summary_width"
case ":$PATH:" in
  *":$flag_prefix:"*) ;;
  *)
    echo
    echo "$(dim "add it to your PATH:")"
    echo "  export PATH=\"$flag_prefix:\$PATH\""
    ;;
esac
