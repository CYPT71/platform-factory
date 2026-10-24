#!/usr/bin/env bash
# Exercise the real signed plugin and native OCI publication against an
# explicitly selected disposable kind cluster and loopback registry.
set -euo pipefail
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
context=${PLATFORM_FACTORY_KIND_CONTEXT:?set an explicit kind context}
registry=${PLATFORM_FACTORY_ACCEPTANCE_REGISTRY:?set the loopback registry with port}
: "${KUBECONFIG:?set an isolated kubeconfig}"
case "$context" in kind-*) ;; *) echo 'acceptance requires an explicit kind context' >&2; exit 2 ;; esac
case "$registry" in localhost:[0-9]*|127.0.0.1:[0-9]*) ;; *) echo 'acceptance requires a loopback registry' >&2; exit 2 ;; esac
# Reject suffixes, credentials and paths even if the shell pattern matched.
python3 - "$registry" <<'PY'
import re,sys
if not re.fullmatch(r'(localhost|127\.0\.0\.1):[0-9]{1,5}',sys.argv[1]): raise SystemExit('invalid loopback registry')
if not 0<int(sys.argv[1].rsplit(':',1)[1])<65536: raise SystemExit('invalid registry port')
PY
for tool in go kubectl python3; do command -v "$tool" >/dev/null; done
work=$(mktemp -d)
evidence=${PLATFORM_FACTORY_EVIDENCE_DIR:-"$repo_root/dist/kind-signed-lifecycle"}
mkdir -p "$evidence"
evidence=$(cd "$evidence" && pwd)
namespace="pf-acceptance-$(date +%s)-$$"
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
(cd plugins/kubernetes && go build -trimpath -o "$work/platform-factory-kubernetes" ./cmd/platform-factory-kubernetes)
"$work/pf" plugin bundle --from plugins/kubernetes --executable "$work/platform-factory-kubernetes" \
 --output "$work/bundle" --key-dir "$work/private-keys" --format json >"$evidence/bundle.json"
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
mkdir -p "$work/project"
cat >"$work/project/pf.yaml" <<YAML
version: 1
language: compiled
profile: static
artifact: app
output: .platform-factory/release/oci-layout
image: $registry/acceptance/app
tag: local
platform: linux/$arch
isolation: container
runtime_engine: podman
ports: ["8080:8080"]
YAML
check_http() {
 local expected=$1
 local log_path="$work/port-forward.log"
 : >"$log_path"
 k port-forward -n "$namespace" --address 127.0.0.1 service/acceptance :8080 >"$log_path" 2>&1 &
 forward_pid=$!
 python3 - "$log_path" "$expected" <<'PY'
import pathlib,re,sys,time,urllib.request
log=pathlib.Path(sys.argv[1]);expected=sys.argv[2];deadline=time.monotonic()+45
last='port-forward not ready'
while time.monotonic()<deadline:
 match=re.search(r'Forwarding from 127\.0\.0\.1:(\d+)',log.read_text())
 if match:
  try:
   with urllib.request.urlopen('http://127.0.0.1:'+match[1]+'/version',timeout=2) as response: actual=response.read().decode().strip()
   if actual==expected: print('HTTP revision='+actual);break
   last='expected '+expected+', got '+actual
  except Exception as e:last=str(e)
 time.sleep(.25)
else:raise SystemExit(last+'\n'+log.read_text())
PY
 kill "$forward_pid" 2>/dev/null || true
 wait "$forward_pid" 2>/dev/null || true
 forward_pid=
}
for revision in v1 v2; do
 (cd "$repo_root" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags="-s -w -X main.revision=$revision" -o "$work/project/app" ./tests/kind/fixtures/service)
 cd "$work/project"
 "$work/pf" build --yes --arch "$arch" --image "$registry/acceptance/app" --tag "$revision" \
  --dist .platform-factory/release --reports .platform-factory/release/reports \
  --rebuild 2 --require-identical --sign-key-dir "$work/private-keys" app >"$evidence/build-$revision.json"
 "$work/pf" publish --yes --insecure-registry --key-dir "$work/private-keys" \
  "$registry/acceptance/app:$revision" >"$evidence/publish-$revision.json"
 "$work/pf" deploy --yes --name acceptance --namespace "$namespace" --workload service --timeout 120s \
  --format json "${plugin_flags[@]}" >"$evidence/deploy-$revision.json"
 check_http "$revision" >"$evidence/http-$revision.txt"
 "$work/pf" logs --tail 30 "${plugin_flags[@]}" >"$evidence/logs-$revision.txt"
 "$work/pf" events "${plugin_flags[@]}" >"$evidence/pf-events-$revision.txt"
 if [ "$revision" = v1 ]; then
  "$work/pf" deploy --yes --name acceptance --namespace "$namespace" --workload service --replicas 2 --timeout 120s --operation-id acceptance-scale-v1 \
   --format json "${plugin_flags[@]}" >"$evidence/scale-v1.json"
  test "$(k get deployment acceptance -n "$namespace" -o jsonpath='{.spec.replicas}')" = 2
  "$work/pf" deploy --yes --name acceptance --namespace "$namespace" --workload service --replicas 2 --timeout 120s --operation-id acceptance-scale-v1 \
   --format json "${plugin_flags[@]}" >"$evidence/retry-v1.json"
  if "$work/pf" deploy --yes --name acceptance --namespace "$namespace" --workload service --replicas 3 --timeout 120s --operation-id acceptance-scale-v1 \
    --format json "${plugin_flags[@]}" >"$evidence/collision-v1.json" 2>"$evidence/collision-v1.stderr"; then
   echo 'a changed mutation reused a completed operation ID' >&2; exit 1
  fi
  grep -Fq 'collides with a different mutation' "$evidence/collision-v1.stderr"
  test "$(k get deployment acceptance -n "$namespace" -o jsonpath='{.spec.replicas}')" = 2
 fi
 # Preserve just public, non-secret receipts before the next build.
 cp .platform-factory/deployed.json "$evidence/deployed-$revision.json"
 rm -rf .platform-factory/release
 done
"$work/pf" rollback --yes --timeout 120s "${plugin_flags[@]}" >"$evidence/rollback.txt"
check_http v1 >"$evidence/http-rollback.txt"
# A new request for a previously deployed digest must apply again, rather than
# replaying a cached success while leaving the rolled-back image running.
"$work/pf" deploy --yes --name acceptance --namespace "$namespace" --workload service --timeout 120s \
 --format json "${plugin_flags[@]}" >"$evidence/redeploy-v2.json"
check_http v2 >"$evidence/http-redeploy-v2.txt"
"$work/pf" rollback --yes --timeout 120s "${plugin_flags[@]}" >"$evidence/rollback-repeat.txt"
check_http v1 >"$evidence/http-rollback-repeat.txt"
k get deployment acceptance -n "$namespace" -o json >"$evidence/deployment-final.json"
python3 - "$evidence" <<'PY'
import hashlib,json,pathlib,sys,urllib.error,urllib.request
root=pathlib.Path(sys.argv[1])
v1=json.loads((root/'publish-v1.json').read_text());v2=json.loads((root/'publish-v2.json').read_text())
assert v1['digest']!=v2['digest'],'revision digests must differ'
for version in ('v1','v2'):
 deployed=json.loads((root/('deploy-'+version+'.json')).read_text())
 published=json.loads((root/('publish-'+version+'.json')).read_text())
 assert deployed['status']=='deployed' and deployed['image']==published['reference']
 assert json.loads((root/('build-'+version+'.json')).read_text())['reproducible']
 # Check the real registry's OCI 1.1 associations, not just CLI success.
 repository=published['reference'].split('@',1)[0]
 host,name=repository.split('/',1)
 base='http://'+host+'/v2/'+name
 try:
  request=urllib.request.Request(base+'/referrers/'+published['digest'],headers={'Accept':'application/vnd.oci.image.index.v1+json'})
  with urllib.request.urlopen(request,timeout=10) as response: index=json.load(response)
 except urllib.error.HTTPError as error:
  if error.code not in (404,405): raise
  request=urllib.request.Request(base+'/manifests/'+published['digest'].replace(':','-',1),headers={'Accept':'application/vnd.oci.image.index.v1+json'})
  with urllib.request.urlopen(request,timeout=10) as response: index=json.load(response)
 expected={'application/vnd.platform-factory.sbom.v1+json','application/vnd.platform-factory.provenance.v1+json','application/vnd.platform-factory.signature.v1+json'}
 assert expected.issubset({item.get('artifactType') for item in index['manifests']})
 for descriptor in index['manifests']:
  request=urllib.request.Request(base+'/manifests/'+descriptor['digest'],headers={'Accept':'application/vnd.oci.image.manifest.v1+json'})
  with urllib.request.urlopen(request,timeout=10) as response: manifest=json.load(response)
  assert manifest['schemaVersion']==2 and manifest['subject']['digest']==published['digest']
  for blob in [manifest['config'],*manifest['layers']]:
   with urllib.request.urlopen(base+'/blobs/'+blob['digest'],timeout=10) as response: payload=response.read()
   assert len(payload)==blob['size'] and 'sha256:'+hashlib.sha256(payload).hexdigest()==blob['digest']
 (root/('registry-referrers-'+version+'.json')).write_text(json.dumps(index,indent=2)+'\n')
final=json.loads((root/'deployment-final.json').read_text())
assert final['spec']['template']['spec']['containers'][0]['image']==v1['reference']
scale=json.loads((root/'scale-v1.json').read_text());retry=json.loads((root/'retry-v1.json').read_text())
assert scale['operation_id']==retry['operation_id']
assert json.loads((root/'deploy-v2.json').read_text())['operation_id']!=json.loads((root/'redeploy-v2.json').read_text())['operation_id']
(root/'result.json').write_text(json.dumps({'status':'passed','scenario':'signed-build-publish-deploy-scale-logs-rollback','revisions':[v1['digest'],v2['digest']],'retry_replayed':True,'conflicting_retry_rejected':True,'redeploy_after_rollback':True,'repeated_rollback':True,'oci11_referrers_verified':True},indent=2)+'\n')
PY
printf 'SIGNED_LIFECYCLE_ACCEPTANCE_OK evidence=%s\n' "$evidence"
