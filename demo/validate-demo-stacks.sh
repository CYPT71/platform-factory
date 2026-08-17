#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
work_root="$(mktemp -d "${TMPDIR:-/tmp}/pf-demo-stacks.XXXXXX")"
cleanup() {
  chmod -R u+w "$work_root" 2>/dev/null || true
  rm -rf -- "$work_root"
}
trap cleanup EXIT
export GOCACHE="$work_root/go-build-cache"

# Exercise the public installer with a relative prefix from outside the repo.
(
  cd "$work_root"
  "$repo_root/install.sh" --components core --prefix ./bin --yes >/dev/null
)
pf="$work_root/bin/pf"
for plugin in go python node; do
  test -x "$work_root/bin/platform-factory-lang-$plugin"
done

mkdir -p "$work_root/projects"/{go,python,javascript,typescript}
printf 'package main\nimport "fmt"\nfunc main(){fmt.Println("Go")}\n' > "$work_root/projects/go/main.go"
printf 'print("Python")\n' > "$work_root/projects/python/app.py"
printf 'console.log("JavaScript");\n' > "$work_root/projects/javascript/index.js"
printf 'const message: string = "TypeScript";\nconsole.log(message);\n' > "$work_root/projects/typescript/index.ts"

for stack in go python javascript typescript; do
  project="$work_root/projects/$stack"
  (
    cd "$project"
    before="$(find . -mindepth 1 -maxdepth 1 | sort)"
    "$pf" init --dry-run . >/dev/null
    test "$(find . -mindepth 1 -maxdepth 1 | sort)" = "$before"
    "$pf" init --yes . >/dev/null
    test -f pf.yaml
    test -f pf.lock
	  test -f .pf/inventory.json
	  test -f .pf/build.pipeline.json
    "$pf" inspect >/dev/null
  )
done

# A mixed repository keeps every plugin observation while --language selects
# only the primary pf.yaml ecosystem. TypeScript and JavaScript intentionally
# share the Node toolchain entry rather than being double-counted.
mixed="$work_root/projects/mixed"
mkdir -p "$mixed"
cp "$work_root/projects/go/main.go" "$mixed/main.go"
cp "$work_root/projects/python/app.py" "$mixed/app.py"
cp "$work_root/projects/javascript/index.js" "$mixed/index.js"
printf 'module example.invalid/mixed\ngo 1.24\n' > "$mixed/go.mod"
"$pf" init --language go --runtime container --yes "$mixed" >/dev/null
python3 - "$mixed/.pf/inventory.json" "$mixed/.pf/build.pipeline.json" <<'PY'
import json, sys
inventory = json.load(open(sys.argv[1]))
pipeline = json.load(open(sys.argv[2]))
assert inventory["api_version"] == "platform-factory.dev/project-inventory/v1"
assert inventory["primary"] == "go"
assert [item["language"] for item in inventory["ecosystems"]] == ["go", "node", "python"]
assert sum(item["selected"] for item in inventory["ecosystems"]) == 1
assert pipeline["api_version"] == "platform-factory.dev/v1"
assert pipeline["stages"][0]["id"] == "build"
PY

# Source archive path: no external tar extraction is used by PF, and a new
# destination becomes a regular initialized project only after full success.
archive_source="$work_root/archive-source"
archive_project="$work_root/projects/from-archive"
mkdir -p "$archive_source"
printf 'print("archive")\n' > "$archive_source/app.py"
tar -czf "$work_root/python-source.tar.gz" -C "$archive_source" app.py
"$pf" init --archive-format tar.gz --extract-to "$archive_project" \
  --language python --runtime container --yes "$work_root/python-source.tar.gz" >/dev/null
test -f "$archive_project/app.py"
test -f "$archive_project/pf.yaml"

# The self-describing filename style is a real first-class path, not an alias:
# exactly one config/lock pair is created and normal discovery consumes it.
long_project="$work_root/projects/long-python"
mkdir -p "$long_project"
printf 'print("long names")\n' > "$long_project/app.py"
"$pf" init --filename-style long --yes "$long_project" >/dev/null
test -f "$long_project/platform-factory.yaml"
test -f "$long_project/platform-factory.lock"
test ! -e "$long_project/pf.yaml"
test ! -e "$long_project/pf.lock"
(cd "$long_project" && "$pf" inspect >/dev/null)

grep -q '^language: go$' "$work_root/projects/go/pf.yaml"
grep -q '^language: python$' "$work_root/projects/python/pf.yaml"
grep -q '^language: node$' "$work_root/projects/javascript/pf.yaml"
grep -Eq '^artifact: "?index\.js"?$' "$work_root/projects/javascript/pf.yaml"
grep -q '^language: node$' "$work_root/projects/typescript/pf.yaml"
grep -Eq '^artifact: "?index\.ts"?$' "$work_root/projects/typescript/pf.yaml"

printf '✅ Go, Python, JavaScript, and TypeScript demos passed from clean directories\n'
