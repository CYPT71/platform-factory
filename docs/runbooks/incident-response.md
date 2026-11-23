# Incident Response Runbook

This document provides step-by-step procedures for responding to incidents in the
platform-factory project, using the diagnostics the CLI actually ships. It assumes
no external monitoring stack (no Grafana, no Prometheus, no PagerDuty, no systemd
units) unless your own deployment added one — platform-factory does not install or
require any of those. If your team layers alerting or a dashboard on top of the
commands below, add that detail to a local copy of this file; don't assume it here.

Every `platform-factory` command accepts `--verbose`, which prints the command's
`trace_id` to stderr before it runs. Structured log lines carry the same
`trace_id` (and `span_id`/`parent_id` for nested operations) as fields - that is
the correlation mechanism to reach for first, in place of a dashboard.

## Table of Contents

1. [General Incident Response Procedure](#general-incident-response-procedure)
2. [Build System Incidents](#build-system-incidents)
3. [Registry Incidents](#registry-incidents)
4. [Execution Incidents](#execution-incidents)
5. [Signing Incidents](#signing-incidents)
6. [Security Incidents](#security-incidents)

## General Incident Response Procedure

### 1. Initial Triage

When an incident is detected:

```bash
# 1. Acknowledge wherever the report came from (GitHub issue, PR comment,
#    CI failure notification) - there is no built-in paging system.

# 2. Start the incident timer
INCIDENT_START=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# 3. Capture the trace_id for the failing command/run, if known - every
#    structured log line and every --verbose invocation carries one, and
#    it is the one identifier that ties CLI, build, and MicroVM evidence
#    together.

# 4. Identify who is investigating (there is one maintainer, @CYPT71;
#    see MAINTAINERS.md for area ownership)

# 5. Begin investigation
```

### 2. Investigation

```bash
# Non-mutating project dashboard: build, evidence, publication, deployment
# state, and the one safe next command
platform-factory status --format json

# Environment/toolchain/sandbox/hypervisor health
platform-factory doctor --json

# Recent structured logs and events (correlate by trace_id)
platform-factory logs --tail 200
platform-factory events --tail 200

# Check recent commits and whether CI passed on them
git log --oneline --since="24 hours ago"
gh run list --branch main --limit 10   # or open the Actions tab directly
```

### 3. Mitigation

```bash
# If the issue is identified and can be fixed quickly:
# 1. Implement the fix
# 2. Verify locally: go build ./..., go vet ./..., go test ./...
# 3. Open a PR; do not push a hotfix directly to main outside an emergency
# 4. Monitor the next few CI runs for recurrence

# If the issue requires more investigation:
# 1. Note a workaround in the issue/PR if one exists
# 2. Continue investigation
# 3. Escalate per MAINTAINERS.md if you cannot resolve it
```

### 4. Resolution

```bash
# Re-run the dashboard/health checks and confirm they're clean
platform-factory status --format json
platform-factory doctor --json

# Document the resolution
INCIDENT_END=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
DURATION=$(( $(date -d "$INCIDENT_END" +%s) - $(date -d "$INCIDENT_START" +%s) ))
echo "Incident duration: $DURATION seconds"
```

### 5. Post-Incident

```bash
# 1. Open a GitHub issue capturing what happened and the trace_id(s) involved
# 2. Identify and assign action items
# 3. Update this runbook if a step above was wrong, missing, or unclear
```

---

## Build System Incidents

### Build Failures

**Symptoms:**
- Builds failing consistently
- Increased build failure rate
- Specific stages failing

platform-factory does not write build logs to a file or run as a daemon: a build
is a single `platform-factory build`/`platform-factory project build` invocation,
and its structured log lines go to stderr (or wherever your CI captures stdio),
tagged with the run's `trace_id`.

**Immediate Actions:**

```bash
# Re-run with --verbose to capture the trace_id and full command line
platform-factory build --verbose [EXECUTABLE]
platform-factory project build --verbose

# Explain the build plan without executing it (dry run)
platform-factory build --dry-run [EXECUTABLE]
platform-factory project build --dry-run

# Check available disk space for the build cache
df -h ~/.cache/platform-factory

# Check memory usage
free -h   # macOS: vm_stat
```

**Common Causes and Solutions:**

1. **Disk Space Exhaustion**
   ```bash
   # Check cache size
   du -sh ~/.cache/platform-factory/* | sort -h

   # The content-addressed cache is safe to delete entirely; the next
   # build repopulates it
   rm -rf ~/.cache/platform-factory
   ```

2. **Memory/CPU Exhaustion**
   ```bash
   # project build accepts resource ceilings; a build that runs away
   # fails closed instead of taking down the host
   platform-factory project build --max-memory 2GiB --max-cpu 5m --max-wall-clock 10m
   ```

3. **Network Issues (fetching toolchains/dependencies during freeze)**
   ```bash
   # Check network connectivity
   curl -v https://registry-1.docker.io

   # Check DNS resolution
   dig registry-1.docker.io

   # Check proxy settings
   env | grep -i proxy
   ```

4. **Dependency/Toolchain Issues**
   ```bash
   # Toolchain and sandbox checks in one place
   platform-factory doctor --json

   # Go-specific toolchain check, if the project is Go
   go version
   echo "$GOTOOLCHAIN"
   ```

### Slow Builds

**Symptoms:**
- Builds taking longer than expected
- Specific stages are slow

There is no built-in `--profile`/`--timing` flag or pprof hook in the build
path; time stages manually and correlate with `trace_id`.

**Immediate Actions:**

```bash
# Time the whole build, or a single project action
time platform-factory build [EXECUTABLE]
time platform-factory project freeze
time platform-factory project build

# Host resource usage during the build
top -c   # or htop if installed
```

**Common Causes and Solutions:**

1. **Inefficient Build Steps**
   - Review the project's `freeze_command` and language-specific freeze
     steps (`platform-factory project freeze --dry-run` explains them
     without running anything).

2. **Network Latency**
   ```bash
   # Test download speed to the registries/mirrors actually in use
   time curl -o /dev/null https://registry-1.docker.io/v2/
   ```

3. **Resource Contention**
   ```bash
   # Cap concurrent/parallel work explicitly via the same budget flags
   # used for exhaustion above
   platform-factory project build --max-cpu 5m
   ```

4. **Cold Cache**
   - Caching is automatic and content-addressed under
     `~/.cache/platform-factory`; a cold cache (first build, or after
     deleting it per the disk-space case above) is the usual explanation
     for an unusually slow run. There is no separate `cache stats`/`cache
     rebuild` command - inspect the directory directly.

---

## Registry Incidents

### Push Failures

**Symptoms:**
- `platform-factory publish` failing to push
- Push timeouts
- Authentication errors

platform-factory has no `registry login`/`check-token`/`refresh-token`
subcommands and no stored token store of its own. Registry credentials come
from `PLATFORM_FACTORY_REGISTRY_USERNAME` / `PLATFORM_FACTORY_REGISTRY_PASSWORD`
(checked by `doctor`) or are passed explicitly to commands that take
`-username`/`-password`.

**Immediate Actions:**

```bash
# Confirm the registry is reachable and credentials resolve
platform-factory doctor --json --registry ghcr.io

# Verbose publish to see exactly what was attempted and its trace_id
platform-factory publish --verbose [LAYOUT] IMAGE:TAG

# Check network connectivity to the registry
curl -v https://ghcr.io/v2/
nc -zv ghcr.io 443
```

**Common Causes and Solutions:**

1. **Authentication Issues**
   ```bash
   # Confirm the expected env vars are actually set in this shell/CI job
   env | grep PLATFORM_FACTORY_REGISTRY

   # doctor's registry-access check reports exactly why a probe failed
   platform-factory doctor --json --registry ghcr.io
   ```

2. **Network Connectivity**
   ```bash
   # Check for a corporate proxy
   env | grep -i proxy

   # Check firewall rules (Linux)
   sudo iptables -L -n
   ```

3. **Registry Rate Limiting**
   ```bash
   # Check rate-limit response headers directly
   curl -I https://ghcr.io/v2/

   # Retry after a backoff; there is no built-in retry/backoff flag on
   # publish today
   sleep 60 && platform-factory publish [LAYOUT] IMAGE:TAG
   ```

4. **Large Image Issues**
   - There is no `--chunk-size` or `image optimize` command. Inspect the
     layout directly instead:
   ```bash
   platform-factory inspect LAYOUT
   ```

### Pull Failures

**Symptoms:**
- `platform-factory import`/the deploying runtime failing to pull
- Digest mismatches

**Immediate Actions:**

```bash
# Inspect the published manifest/digest by reference
platform-factory registry inspect IMAGE@sha256:DIGEST

# Confirm the layout's own digest before it was ever pushed
platform-factory inspect LAYOUT
```

**Common Causes and Solutions:**

1. **Image Not Found / Typo in Reference**
   ```bash
   platform-factory registry inspect IMAGE@sha256:DIGEST
   # a 404 here means the reference, not the client, is wrong
   ```

2. **Digest Mismatch**
   ```bash
   # Re-verify the release evidence against the digest actually deployed
   platform-factory verify-release --evidence evidence.json LAYOUT

   # Re-publish from the original layout rather than hand-editing a tag
   platform-factory publish [LAYOUT] IMAGE:TAG
   ```

3. **Network Issues**
   - Same troubleshooting as push failures above.

---

## Execution Incidents

### MicroVM Failures

**Symptoms:**
- MicroVMs failing to start
- MicroVMs crashing
- Execution errors

**Immediate Actions:**

```bash
# Hypervisor/sandbox capability check - this is the first thing to run
platform-factory doctor --json

# MicroVM-specific status and logs (replace <id> with the container/VM id)
platform-factory microvm status <id>
platform-factory microvm logs <id>

# Raw virtualization support, if doctor's native-hypervisor check fails
lsmod | grep kvm                      # Linux/KVM
sw_vers                               # macOS, to check HVF eligibility
grep -E 'vmx|svm' /proc/cpuinfo       # Linux: hardware virtualization present
```

**Common Causes and Solutions:**

1. **Virtualization Not Enabled / Not Available**
   ```bash
   # doctor's native-hypervisor check (kvm-device, kvm-extensions,
   # hyper-v, virtualization-framework) explains exactly what is missing
   platform-factory doctor --json
   ```

2. **KVM Not Available (Linux)**
   ```bash
   lsmod | grep kvm
   sudo modprobe kvm
   sudo modprobe kvm_intel  # or kvm_amd
   ls -l /dev/kvm           # permissions: the invoking user needs rw access
   ```

3. **HVF Not Available (macOS)**
   ```bash
   sw_vers
   # HVF is a framework entitlement, not a loadable kext on current
   # macOS; doctor's virtualization-framework check is authoritative here
   ```

4. **Resource Issues**
   ```bash
   free -h
   # Direct, low-level native-MicroVM invocation (memory in MiB, not GiB)
   platform-factory microvm run --memory-mib 2048 ...
   # A pf.yaml project instead launches with isolation: microvm already
   # set in its config; adjust memory/vcpus there, then:
   platform-factory launch --isolation=microvm
   ```

### MicroVM Performance Issues

**Symptoms:**
- Slow MicroVM startup
- High CPU usage
- Memory pressure

**Immediate Actions:**

```bash
platform-factory microvm status <id>
platform-factory microvm logs <id>

top -c      # or htop
mpstat -P ALL 1   # Linux: per-CPU, to spot throttling/contention
```

**Common Causes and Solutions:**

1. **CPU/Memory Pressure**
   ```bash
   platform-factory microvm run --vcpus 2 --memory-mib 4096 ...
   ```

2. **Disk I/O Bottleneck**
   ```bash
   iostat -x 1
   ```
   There is no built-in `--storage` override; this is a host/placement
   decision, not a platform-factory flag.

---

## Signing Incidents

### Signing Failures

**Symptoms:**
- `platform-factory publish --sign` failing
- Signature verification failures
- Key-related errors

platform-factory has no `key generate`/`key import`/`trust add` subcommands.
Local signing uses a `signing.FileKeyStore` directory passed with
`--key-dir`/`--key-name`; production releases sign keylessly through GitHub
Actions OIDC/Fulcio (see SECURITY.md) - there is no long-lived private key to
manage for those.

**Immediate Actions:**

```bash
# Verbose publish/verify-release to see exactly which key material was
# loaded and from where
platform-factory publish --verbose --sign --key-dir KEYDIR --key-name NAME [LAYOUT] IMAGE:TAG
platform-factory verify-release --verbose --key-dir KEYDIR --key-name NAME --evidence evidence.json LAYOUT

# Check the key directory exists and has sane permissions
ls -la KEYDIR
```

**Common Causes and Solutions:**

1. **Missing or Misnamed Key**
   ```bash
   ls KEYDIR
   # --key-name defaults to "release"; confirm the file matches
   ```

2. **Key Permissions**
   ```bash
   chmod 600 KEYDIR/*
   chmod 700 KEYDIR
   ```

3. **CI (keyless) Signing Identity Issues**
   - See SECURITY.md's "Signing identity and rotation" section: there is
     no key to rotate for CI releases, only trusted workflow identity,
     repository access, and token permissions to review.

### Verification Failures

**Symptoms:**
- `platform-factory verify-release` failing
- Deploys refused due to policy/evidence mismatch

**Immediate Actions:**

```bash
platform-factory verify-release --verbose --evidence evidence.json --policy policy.json LAYOUT

# Inspect what the layout and its SBOM actually contain
platform-factory inspect LAYOUT
platform-factory sbom LAYOUT
```

**Common Causes and Solutions:**

1. **Layout Changed After Signing**
   ```bash
   # Re-run the full publish (which re-signs) rather than editing
   # anything by hand
   platform-factory publish --sign [LAYOUT] IMAGE:TAG
   ```

2. **Wrong Certificate/Key**
   ```bash
   # Pass the exact certificate chain verify-release expects
   platform-factory verify-release --certificate cert.pem --intermediate-certificate chain.pem ...
   ```

3. **Evidence/Journal Mismatch**
   ```bash
   # --journal requires the provenance to match the pipeline journal
   # exactly; regenerate the journal from the same run rather than reusing
   # an older one
   platform-factory verify-release --journal journal.json ...
   ```

---

## Security Incidents

### Vulnerability Disclosure

**Symptoms:**
- Security vulnerability reported
- Suspicious activity detected
- Unauthorized access

**Immediate Actions:**

```bash
# 1. DO NOT discuss in public issues, PRs, or Discussions
# 2. Use GitHub's private security advisory reporting for
#    CYPT71/platform-factory - this is the one real, documented channel
#    (see SECURITY.md); there is no separate PagerDuty/Slack flow
# 3. Limit discussion to the advisory and anyone explicitly added to it

# Acknowledge the report (if external)
# "Thank you for your report. We are investigating and will respond shortly."
```

**Response Procedure:** follows SECURITY.md directly - receipt acknowledged
within three business days, an initial assessment targeted within seven;
confirmed issues are coordinated privately until a fix and advisory are ready.
Those are operational goals, not contractual SLAs.

1. **Triage** - assess severity/impact, confirm it reproduces, identify
   affected components.
2. **Containment** - if there's an ongoing exploit, revoke whatever
   credential or access enabled it (GitHub tokens, registry credentials,
   signing identities) before anything else.
3. **Remediation** - fix, test, and prepare the fix for release.
4. **Disclosure** - coordinate with the reporter, prepare the advisory,
   release the fix and the advisory together.

**Contact:** GitHub private security advisory on this repository. See
SECURITY.md for the current supported-versions and disclosure policy, and
MAINTAINERS.md for area ownership.

---

## Escalation Path

If you cannot resolve an incident:

1. Comment on the relevant issue/PR, or open a new issue, with the
   `trace_id` and what you've already ruled out.
2. For anything security-sensitive, use GitHub's private security advisory
   flow instead of a public issue (see above).
3. This is currently a single-maintainer project (@CYPT71, see
   MAINTAINERS.md) - there is no multi-tier on-call rotation to escalate
   through beyond that.

---

## Tools and Commands Reference

### Health and Status

```bash
# Non-mutating project dashboard
platform-factory status --format json

# Environment/toolchain/sandbox/hypervisor/registry checks
platform-factory doctor --json
platform-factory doctor --json --registry ghcr.io
platform-factory doctor --json --policy policy.json
```

### Logs and Events

```bash
platform-factory logs --tail 200
platform-factory logs --follow

platform-factory events --tail 200
platform-factory events --follow
```

### MicroVM Diagnostics

```bash
platform-factory microvm status <id>
platform-factory microvm logs <id>
platform-factory microvm restart <id>
platform-factory microvm stop <id>
```

There is no metrics-scrape endpoint (no `:9090/metrics`, no built-in
Prometheus exporter) in platform-factory itself; `--verbose` plus the
structured `trace_id`-correlated log lines are the actual instrumentation
this project ships today. Exporting them to a metrics/tracing backend of
your choice is left to your own deployment.

---

## Runbook Maintenance

This runbook should be updated:
- After each incident (add lessons learned)
- When commands referenced here change or are removed
- When new features are added (add new incident types)
- Quarterly (review and update)

**Last Updated**: 2026-11-23
**Owner**: @CYPT71
