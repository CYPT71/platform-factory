#!/usr/bin/env bash
# Exercise the full Python project lifecycle (pf init -> provision-runtime ->
# freeze -> build -> status -> publish -> deploy -> logs/events -> failure &
# recovery -> v2 -> rollback -> redeploy -> rollback) against an explicitly
# selected disposable kind cluster and loopback OCI 1.1 registry, without any
# hand-written Kubernetes manifest or precompiled application binary.
set -euo pipefail
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
context=${PLATFORM_FACTORY_KIND_CONTEXT:?set an explicit kind context}
registry=${PLATFORM_FACTORY_ACCEPTANCE_REGISTRY:?set the loopback registry with port}
: "${KUBECONFIG:?set an isolated kubeconfig}"
python_base_image=${PLATFORM_FACTORY_PYTHON_BASE_IMAGE:-"python@sha256:191083cdf6c7bda8c76158983163ead5d5cf7b77cf3033e805bb0f5296f51b4b"}
case "$context" in kind-*) ;; *) echo 'acceptance requires an explicit kind context' >&2; exit 2 ;; esac
case "$registry" in localhost:[0-9]*|127.0.0.1:[0-9]*) ;; *) echo 'acceptance requires a loopback registry' >&2; exit 2 ;; esac
# Reject suffixes, credentials and paths even if the shell pattern matched.
python3 - "$registry" "$python_base_image" <<'PY'
import re, sys
if not re.fullmatch(r'(localhost|127\.0\.0\.1):[0-9]{1,5}', sys.argv[1]):
    raise SystemExit('invalid loopback registry')
if not 0 < int(sys.argv[1].rsplit(':', 1)[1]) < 65536:
    raise SystemExit('invalid registry port')
if not re.search(r'@sha256:[0-9a-f]{64}$', sys.argv[2]):
    raise SystemExit('PLATFORM_FACTORY_PYTHON_BASE_IMAGE must be digest-pinned')
PY
for tool in go kubectl python3; do command -v "$tool" >/dev/null; done
work=$(mktemp -d)
evidence=${PLATFORM_FACTORY_EVIDENCE_DIR:-"$repo_root/dist/kind-python-lifecycle"}
mkdir -p "$evidence"
evidence=$(cd "$evidence" && pwd)
namespace="pf-py-acceptance-$(date +%s)-$$"
created=0
forward_pid=
k() { kubectl --context "$context" "$@"; }
cleanup() {
  if [ -n "$forward_pid" ]; then kill "$forward_pid" 2>/dev/null || true; wait "$forward_pid" 2>/dev/null || true; fi
  if [ "$created" = 1 ]; then
    k get pods -n "$namespace" -o wide >"$evidence/pods.txt" 2>&1 || true
    k get events -n "$namespace" --sort-by=.lastTimestamp >"$evidence/events.txt" 2>&1 || true
    k delete namespace "$namespace" --wait=false >/dev/null 2>&1 || true
  fi
  rm -rf -- "$work"
}
trap cleanup EXIT
arch=$(k get nodes -o jsonpath='{.items[0].status.nodeInfo.architecture}')
case "$arch" in amd64|arm64) ;; *) echo "unsupported cluster architecture $arch" >&2; exit 2 ;; esac
k wait --for=condition=Ready node --all --timeout=120s
k create namespace "$namespace"
created=1
k get nodes -o wide >"$evidence/nodes.txt"

cd "$repo_root"
go build -trimpath -o "$work/pf" ./cmd/platform-factory
(cd plugins/lang-python && GOWORK=off go build -trimpath -o "$work/platform-factory-lang-python" .)
(cd plugins/kubernetes && go build -trimpath -o "$work/platform-factory-kubernetes" ./cmd/platform-factory-kubernetes)
# Isolate language plugin discovery to the freshly built binary beside $work/pf.
export PLATFORM_FACTORY_LANG_PLUGIN_DIR="$work/lang-plugins"
mkdir -p "$PLATFORM_FACTORY_LANG_PLUGIN_DIR"

mkdir -p "$work/project/.platform-factory/keys"
chmod 700 "$work/project/.platform-factory/keys"
"$work/pf" plugin bundle --from plugins/kubernetes --executable "$work/platform-factory-kubernetes" \
  --output "$work/bundle" --key-dir "$work/project/.platform-factory/keys" --key-name release \
  --format json >"$evidence/bundle.json"
"$work/pf" plugin verify --from "$work/bundle" --key "$work/bundle/publisher.pem" --format json >"$evidence/verify.json"
"$work/pf" plugin install --from "$work/bundle" --key "$work/bundle/publisher.pem" --plugin-dir "$work/plugins" --format json >"$evidence/install.json"
plugin_flags=(--plugin-dir "$work/plugins" --plugin-key "$work/bundle/publisher.pem")
if [ "${PLATFORM_FACTORY_ACCEPTANCE_ALLOW_UNSANDBOXED:-0}" = 1 ]; then plugin_flags+=(--allow-unsandboxed-plugin); fi
export PLATFORM_FACTORY_OPERATION_JOURNAL_DIR="$work/journal"
export PLATFORM_FACTORY_WORKLOAD_STATE_DIR="$work/workloads"
export PLATFORM_FACTORY_UPLOAD_SESSION_DIR="$work/uploads"
# Bind the plugin's kubeconfig to the explicitly selected context, without
# copying credentials into evidence or changing the user's current context.
k config view --minify --raw --flatten >"$work/kubeconfig"
chmod 600 "$work/kubeconfig"
export KUBECONFIG="$work/kubeconfig"

write_python_app() {
  local rev=$1
  local fail_mode=${2:-0}
  cat >"$work/project/app.py" <<PY
import http.server
import signal
import sys

REVISION = "${rev}"
FAIL_ON_START = ${fail_mode}

if FAIL_ON_START:
    print(f"acceptance-python revision={REVISION} status=failing-by-design", flush=True)
    sys.exit(1)

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/healthz":
            body = b"ok\n"
        elif self.path == "/version":
            body = (REVISION + "\n").encode("utf-8")
        else:
            self.send_response(404)
            self.end_headers()
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        print(f"acceptance-python revision={REVISION} request=" + (fmt % args), flush=True)

server = http.server.ThreadingHTTPServer(("0.0.0.0", 8080), Handler)

def _stop(_signum, _frame):
    raise SystemExit(0)

signal.signal(signal.SIGTERM, _stop)
signal.signal(signal.SIGINT, _stop)
print(f"acceptance-python revision={REVISION} listen=:8080", flush=True)
try:
    server.serve_forever()
except SystemExit:
    pass
finally:
    server.server_close()
PY
}

write_python_app v1 0
cd "$work/project"
"$work/pf" init --yes --language python --dependency-mode none --engine podman . >"$evidence/init.txt"
cat >>"$work/project/pf.yaml" <<YAML
image: $registry/acceptance/python-app
tag: local
platform: linux/$arch
ports: ["8080:8080"]
YAML
mkdir -p "$work/project/policies"
cat >"$work/project/policies/build.json" <<'JSON'
{
  "api_version": "platform-factory.dev/policy/v1",
  "require_sbom": true,
  "require_provenance": true,
  "require_signature": true
}
JSON

"$work/pf" plugin provision-runtime --language python \
  --image "$python_base_image" --dir "$work/project" --arch "$arch" \
  --format json >"$evidence/provision-runtime.json"

check_http() {
  local expected=$1
  local log_path="$work/port-forward.log"
  : >"$log_path"
  k port-forward -n "$namespace" --address 127.0.0.1 service/acceptance-py :8080 >"$log_path" 2>&1 &
  forward_pid=$!
  python3 - "$log_path" "$expected" <<'PY'
import pathlib, re, sys, time, urllib.request
log = pathlib.Path(sys.argv[1])
expected = sys.argv[2]
deadline = time.monotonic() + 60
last = 'port-forward not ready'
while time.monotonic() < deadline:
    match = re.search(r'Forwarding from 127\.0\.0\.1:(\d+)', log.read_text())
    if match:
        base = 'http://127.0.0.1:' + match[1]
        try:
            with urllib.request.urlopen(base + '/healthz', timeout=2) as health:
                if health.read().decode().strip() != 'ok':
                    last = 'unexpected /healthz body'
                    time.sleep(.25)
                    continue
            with urllib.request.urlopen(base + '/version', timeout=2) as response:
                actual = response.read().decode().strip()
            if actual == expected:
                print('HTTP revision=' + actual)
                break
            last = 'expected ' + expected + ', got ' + actual
        except Exception as e:
            last = str(e)
    time.sleep(.25)
else:
    raise SystemExit(last + '\n' + log.read_text())
PY
  kill "$forward_pid" 2>/dev/null || true
  wait "$forward_pid" 2>/dev/null || true
  forward_pid=
}

build_and_publish() {
  local rev=$1
  "$work/pf" project freeze >"$evidence/freeze-$rev.json"
  "$work/pf" build --yes >"$evidence/build-$rev.json"
  "$work/pf" status --format json >"$evidence/status-built-$rev.json"
  "$work/pf" publish --yes --insecure-registry --key-dir "$work/project/.platform-factory/keys" \
    "$registry/acceptance/python-app:$rev" >"$evidence/publish-$rev.json"
  "$work/pf" status --format json >"$evidence/status-published-$rev.json"
}

# 1. Revision v1: freeze -> build -> status -> publish -> deploy -> HTTP -> logs/events -> scale/retry/collision
build_and_publish v1
"$work/pf" deploy --yes --name acceptance-py --namespace "$namespace" --workload service --timeout 120s \
  --format json "${plugin_flags[@]}" >"$evidence/deploy-v1.json"
"$work/pf" status --format json >"$evidence/status-deployed-v1.json"
check_http v1 >"$evidence/http-v1.txt"
"$work/pf" logs --tail 30 "${plugin_flags[@]}" >"$evidence/logs-v1.txt"
grep -Fq 'acceptance-python revision=v1' "$evidence/logs-v1.txt"
"$work/pf" events "${plugin_flags[@]}" >"$evidence/pf-events-v1.txt"

"$work/pf" deploy --yes --name acceptance-py --namespace "$namespace" --workload service --replicas 2 --timeout 120s \
  --operation-id acceptance-py-scale-v1 --format json "${plugin_flags[@]}" >"$evidence/scale-v1.json"
test "$(k get deployment acceptance-py -n "$namespace" -o jsonpath='{.spec.replicas}')" = 2
"$work/pf" deploy --yes --name acceptance-py --namespace "$namespace" --workload service --replicas 2 --timeout 120s \
  --operation-id acceptance-py-scale-v1 --format json "${plugin_flags[@]}" >"$evidence/retry-v1.json"
if "$work/pf" deploy --yes --name acceptance-py --namespace "$namespace" --workload service --replicas 3 --timeout 120s \
    --operation-id acceptance-py-scale-v1 --format json "${plugin_flags[@]}" >"$evidence/collision-v1.json" 2>"$evidence/collision-v1.stderr"; then
  echo 'a changed mutation reused a completed operation ID' >&2
  exit 1
fi
grep -Fq 'collides with a different mutation' "$evidence/collision-v1.stderr"
test "$(k get deployment acceptance-py -n "$namespace" -o jsonpath='{.spec.replicas}')" = 2
cp .platform-factory/deployed.json "$evidence/deployed-v1.json"

# 2. Failure & rollback recovery: deploy a failing Python revision, observe
#    rollout failure, then roll back cleanly to v1.
write_python_app bad 1
rm -rf .platform-factory/release .platform-factory/image
build_and_publish bad
if "$work/pf" deploy --yes --name acceptance-py --namespace "$namespace" --workload service --timeout 20s \
    --format json "${plugin_flags[@]}" >"$evidence/deploy-bad.json" 2>"$evidence/deploy-bad.stderr"; then
  echo 'failing Python revision unexpectedly succeeded rollout' >&2
  exit 1
fi
"$work/pf" rollback --yes --timeout 120s "${plugin_flags[@]}" >"$evidence/rollback-from-bad.txt"
check_http v1 >"$evidence/http-rollback-from-bad.txt"

# 3. Revision v2 -> rollback to v1 -> redeploy v2 -> second rollback to v1
write_python_app v2 0
rm -rf .platform-factory/release .platform-factory/image
build_and_publish v2
"$work/pf" deploy --yes --name acceptance-py --namespace "$namespace" --workload service --timeout 120s \
  --format json "${plugin_flags[@]}" >"$evidence/deploy-v2.json"
"$work/pf" status --format json >"$evidence/status-deployed-v2.json"
check_http v2 >"$evidence/http-v2.txt"
"$work/pf" logs --tail 30 "${plugin_flags[@]}" >"$evidence/logs-v2.txt"
grep -Fq 'acceptance-python revision=v2' "$evidence/logs-v2.txt"
"$work/pf" events "${plugin_flags[@]}" >"$evidence/pf-events-v2.txt"
cp .platform-factory/deployed.json "$evidence/deployed-v2.json"

"$work/pf" rollback --yes --timeout 120s "${plugin_flags[@]}" >"$evidence/rollback.txt"
check_http v1 >"$evidence/http-rollback.txt"

# Redeploy v2 reusing the persisted published digest, then roll back again.
"$work/pf" deploy --yes --name acceptance-py --namespace "$namespace" --workload service --timeout 120s \
  --format json "${plugin_flags[@]}" >"$evidence/redeploy-v2.json"
check_http v2 >"$evidence/http-redeploy-v2.txt"
"$work/pf" rollback --yes --timeout 120s "${plugin_flags[@]}" >"$evidence/rollback-repeat.txt"
check_http v1 >"$evidence/http-rollback-repeat.txt"
k get deployment acceptance-py -n "$namespace" -o json >"$evidence/deployment-final.json"

python3 - "$evidence" "$namespace" <<'PY'
import hashlib, json, pathlib, sys, urllib.error, urllib.request
root = pathlib.Path(sys.argv[1])
namespace = sys.argv[2]

provision = json.loads((root / 'provision-runtime.json').read_text())
assert provision['status'] == 'provisioned' and provision['language'] == 'python'

v1 = json.loads((root / 'publish-v1.json').read_text())
v2 = json.loads((root / 'publish-v2.json').read_text())
assert v1['digest'] != v2['digest'], 'revision digests must differ'

for version in ('v1', 'v2'):
    built_status = json.loads((root / f'status-built-{version}.json').read_text())
    pub_status = json.loads((root / f'status-published-{version}.json').read_text())
    dep_status = json.loads((root / f'status-deployed-{version}.json').read_text())
    published = json.loads((root / f'publish-{version}.json').read_text())
    deployed = json.loads((root / f'deploy-{version}.json').read_text())

    assert built_status['initialized'] and built_status['built'] and built_status['evidence_complete'], built_status
    assert pub_status['published'] and pub_status['published_reference'] == published['reference'], pub_status
    assert dep_status['deployed'] and dep_status['deployment'] == f'{namespace}/acceptance-py', dep_status
    assert deployed['status'] == 'deployed' and deployed['image'] == published['reference'], deployed

    repository = published['reference'].split('@', 1)[0]
    host, name = repository.split('/', 1)
    base = 'http://' + host + '/v2/' + name
    try:
        request = urllib.request.Request(
            base + '/referrers/' + published['digest'],
            headers={'Accept': 'application/vnd.oci.image.index.v1+json'},
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            index = json.load(response)
    except urllib.error.HTTPError as error:
        if error.code not in (404, 405):
            raise
        request = urllib.request.Request(
            base + '/manifests/' + published['digest'].replace(':', '-', 1),
            headers={'Accept': 'application/vnd.oci.image.index.v1+json'},
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            index = json.load(response)
    expected = {
        'application/vnd.platform-factory.sbom.v1+json',
        'application/vnd.platform-factory.provenance.v1+json',
        'application/vnd.platform-factory.signature.v1+json',
    }
    assert expected.issubset({item.get('artifactType') for item in index['manifests']}), index
    for descriptor in index['manifests']:
        request = urllib.request.Request(
            base + '/manifests/' + descriptor['digest'],
            headers={'Accept': 'application/vnd.oci.image.manifest.v1+json'},
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            manifest = json.load(response)
        assert manifest['schemaVersion'] == 2 and manifest['subject']['digest'] == published['digest']
        for blob in [manifest['config'], *manifest['layers']]:
            with urllib.request.urlopen(base + '/blobs/' + blob['digest'], timeout=10) as response:
                payload = response.read()
            assert len(payload) == blob['size'] and 'sha256:' + hashlib.sha256(payload).hexdigest() == blob['digest']
    (root / f'registry-referrers-{version}.json').write_text(json.dumps(index, indent=2) + '\n')

final = json.loads((root / 'deployment-final.json').read_text())
assert final['spec']['template']['spec']['containers'][0]['image'] == v1['reference']
scale = json.loads((root / 'scale-v1.json').read_text())
retry = json.loads((root / 'retry-v1.json').read_text())
assert scale['operation_id'] == retry['operation_id']
assert json.loads((root / 'deploy-v2.json').read_text())['operation_id'] != json.loads((root / 'redeploy-v2.json').read_text())['operation_id']

(root / 'result.json').write_text(json.dumps({
    'status': 'passed',
    'scenario': 'python-init-provision-freeze-build-status-publish-deploy-failure-rollback',
    'revisions': [v1['digest'], v2['digest']],
    'failed_revision_rejected_and_rolled_back': True,
    'retry_replayed': True,
    'conflicting_retry_rejected': True,
    'redeploy_after_rollback': True,
    'repeated_rollback': True,
    'oci11_referrers_verified': True,
}, indent=2) + '\n')
PY
printf 'PYTHON_LIFECYCLE_ACCEPTANCE_OK evidence=%s\n' "$evidence"
