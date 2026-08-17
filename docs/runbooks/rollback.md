# Rollback Procedures

This document provides step-by-step procedures for rolling back deployments and
reverting to previous versions of platform-factory components.

## Table of Contents

1. [General Rollback Principles](#general-rollback-principles)
2. [Version Identification](#version-identification)
3. [Component-Specific Rollback Procedures](#component-specific-rollback-procedures)
4. [Rollback Testing](#rollback-testing)
5. [Post-Rollback Procedures](#post-rollback-procedures)

## General Rollback Principles

### When to Rollback

Rollback should be performed when:
- A deployment causes critical failures
- A new version introduces security vulnerabilities
- Performance degrades below acceptable levels
- Data corruption is detected
- User impact is severe and immediate

### Rollback Decision Tree

```
┌─────────────────────────────────────┐
│  Is the issue critical?               │
└───────────────────┬─────────────────┘
                    │
         Yes         │         No
                    │
         ▼          │          ▼
┌─────────────────────────────────────┐
│  Can it be fixed with a hot patch?    │
└───────────────────┬─────────────────┘
                    │
         No         │         Yes
                    │
         ▼          │          ▼
┌─────────────────────────────────────┐
│  Rollback to previous version         │   Apply hot patch
└─────────────────────────────────────┘
```

### Rollback Safety Checklist

Before performing a rollback, verify:

- [ ] The previous version is known to be stable
- [ ] The previous version's artifacts are available
- [ ] Data migration (if any) can be reversed
- [ ] Rollback procedure has been tested
- [ ] Stakeholders have been notified
- [ ] Monitoring is in place to verify rollback success

## Version Identification

### Current Version

```bash
# Check platform-factory version
./platform-factory version

# Check control plane version
./platform-factory-control-plane version

# Check worker version
./platform-factory-worker version
```

### Previous Versions

```bash
# List available versions
git tag | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V

# Check what's installed
ls -la /usr/local/bin/platform-factory*

# Check release assets
ls -la dist/
```

### Version Compatibility

| Component | Current | Previous | Compatible |
|-----------|---------|----------|------------|
| platform-factory | v4.0.0 | v3.2.1 | ✅ Yes |
| control-plane | v4.0.0 | v3.2.1 | ✅ Yes |
| worker | v4.0.0 | v3.2.1 | ✅ Yes |

**Note**: Always roll back all components to the same version to ensure compatibility.

## Component-Specific Rollback Procedures

### 1. platform-factory CLI Rollback

#### Automatic Rollback (Recommended)

```bash
# List installed versions
./scripts/list-installed-versions.sh

# Rollback to previous version
sudo ./scripts/rollback-cli.sh v3.2.1

# Verify rollback
./platform-factory version
```

#### Manual Rollback

```bash
# 1. Download previous version
PREV_VERSION="v3.2.1"
wget https://github.com/CYPT71/platform-factory/releases/download/${PREV_VERSION}/platform-factory-${PREV_VERSION}-$(uname -s)-$(uname -m)

# 2. Install previous version
chmod +x platform-factory-${PREV_VERSION}-*
sudo mv platform-factory-${PREV_VERSION}-* /usr/local/bin/platform-factory

# 3. Verify installation
./platform-factory version

# 4. Clean up
rm -f platform-factory-${PREV_VERSION}-*
```

#### Configuration Rollback

```bash
# Backup current configuration
cp -r ~/.config/platform-factory ~/.config/platform-factory-backup-$(date +%Y%m%d-%H%M%S)

# Restore previous configuration (if available)
cp -r ~/.config/platform-factory-backups/v3.2.1 ~/.config/platform-factory

# Verify configuration
./platform-factory config validate
```

### 2. Control Plane Rollback

#### Using Systemd (Recommended)

```bash
# 1. Stop current service
sudo systemctl stop platform-factory-control-plane

# 2. Rollback binary
sudo cp /usr/local/bin/platform-factory-control-plane-backups/platform-factory-control-plane-v3.2.1 /usr/local/bin/platform-factory-control-plane

# 3. Reload systemd
sudo systemctl daemon-reload

# 4. Start service
sudo systemctl start platform-factory-control-plane

# 5. Verify
sudo systemctl status platform-factory-control-plane
./platform-factory-control-plane version
```

#### Using Docker

```bash
# 1. Stop current container
docker stop platform-factory-control-plane

# 2. Remove current container
docker rm platform-factory-control-plane

# 3. Pull previous version
docker pull ghcr.io/cypt71/platform-factory-control-plane:v3.2.1

# 4. Start previous version
docker run -d \
  --name platform-factory-control-plane \
  -v /path/to/config:/config \
  -v /path/to/state:/state \
  --network host \
  ghcr.io/cypt71/platform-factory-control-plane:v3.2.1

# 5. Verify
docker logs platform-factory-control-plane
```

#### Kubernetes Rollback

```bash
# 1. Check deployment history
kubectl rollout history deployment/platform-factory-control-plane

# 2. Rollback to previous revision
kubectl rollout undo deployment/platform-factory-control-plane

# 3. Or rollback to specific revision
kubectl rollout undo deployment/platform-factory-control-plane --to-revision=2

# 4. Monitor rollout status
kubectl rollout status deployment/platform-factory-control-plane

# 5. Verify
kubectl get pods -l app=platform-factory-control-plane
```

### 3. Worker Rollback

#### Individual Worker Rollback

```bash
# 1. Identify worker
./platform-factory-worker id

# 2. Stop worker
sudo systemctl stop platform-factory-worker@<worker-id>

# 3. Rollback binary
sudo cp /usr/local/bin/platform-factory-worker-backups/platform-factory-worker-v3.2.1 /usr/local/bin/platform-factory-worker

# 4. Start worker
sudo systemctl start platform-factory-worker@<worker-id>

# 5. Verify
sudo systemctl status platform-factory-worker@<worker-id>
./platform-factory-worker version
```

#### All Workers Rollback

```bash
# 1. Stop all workers
for worker in $(systemctl list-units --type=service | grep platform-factory-worker | awk '{print $1}'); do
  sudo systemctl stop $worker
done

# 2. Rollback binary for all
sudo cp /usr/local/bin/platform-factory-worker-backups/platform-factory-worker-v3.2.1 /usr/local/bin/platform-factory-worker

# 3. Start all workers
for worker in $(systemctl list-units --type=service | grep platform-factory-worker | awk '{print $1}'); do
  sudo systemctl start $worker
done

# 4. Verify all workers
./scripts/verify-workers.sh
```

#### Kubernetes Worker Rollback

```bash
# 1. Check deployment history
kubectl rollout history deployment/platform-factory-worker

# 2. Rollback workers
kubectl rollout undo deployment/platform-factory-worker

# 3. Monitor rollout
kubectl rollout status deployment/platform-factory-worker

# 4. Verify workers are ready
kubectl get pods -l app=platform-factory-worker
```

### 4. Database/State Rollback

**WARNING**: Database rollback can cause data loss. Only perform if you have a
backup and understand the implications.

#### CAS (Content Addressable Storage) Rollback

```bash
# 1. Backup current state
./platform-factory cas backup /path/to/backup-$(date +%Y%m%d-%H%M%S)

# 2. Restore from previous backup
./platform-factory cas restore /path/to/backup-v3.2.1

# 3. Verify consistency
./platform-factory cas verify
```

#### Control Plane State Rollback

```bash
# 1. Backup current state
cp /var/lib/platform-factory/control-plane-state.json /var/lib/platform-factory/control-plane-state.json.backup-$(date +%Y%m%d-%H%M%S)

# 2. Restore previous state
cp /var/lib/platform-factory/backups/control-plane-state-v3.2.1.json /var/lib/platform-factory/control-plane-state.json

# 3. Set correct permissions
chmod 600 /var/lib/platform-factory/control-plane-state.json
chown platform-factory:platform-factory /var/lib/platform-factory/control-plane-state.json

# 4. Restart control plane
sudo systemctl restart platform-factory-control-plane
```

### 5. Full System Rollback

```bash
# 1. Stop all services
sudo systemctl stop platform-factory-control-plane
for worker in $(systemctl list-units --type=service | grep platform-factory-worker | awk '{print $1}'); do
  sudo systemctl stop $worker
done

# 2. Rollback all binaries
sudo ./scripts/rollback-all.sh v3.2.1

# 3. Restore all configurations
sudo ./scripts/restore-configs.sh v3.2.1

# 4. Restore state (if needed)
sudo ./scripts/restore-state.sh v3.2.1

# 5. Start all services
sudo systemctl start platform-factory-control-plane
for worker in $(systemctl list-units --type=service | grep platform-factory-worker | awk '{print $1}'); do
  sudo systemctl start $worker
done

# 6. Verify all components
./scripts/verify-installation.sh
```

## Rollback Testing

### Pre-Rollback Verification

```bash
# 1. Check system health before rollback
./scripts/health-check.sh

# 2. Backup all state
./scripts/backup-all.sh v4.0.0-rollback-$(date +%Y%m%d-%H%M%S)

# 3. Verify backups
./scripts/verify-backups.sh v4.0.0-rollback-$(date +%Y%m%d-%H%M%S)

# 4. Check current version functionality
./scripts/verify-version.sh v4.0.0
```

### Rollback Verification

```bash
# 1. Verify version
./platform-factory version

# 2. Run health checks
./scripts/health-check.sh

# 3. Run smoke tests
./scripts/smoke-test.sh

# 4. Verify data consistency
./scripts/verify-data.sh

# 5. Check specific functionality
./platform-factory build --test examples/hello-world
./platform-factory push --test ghcr.io/your-repo/test:rollback
```

### Automated Rollback Test

```bash
# Test rollback procedure in staging
export ENVIRONMENT=staging
./scripts/test-rollback.sh v3.2.1

# Verify rollback in staging
./scripts/verify-rollback.sh v3.2.1
```

## Post-Rollback Procedures

### 1. Verification

```bash
# Run comprehensive verification
./scripts/post-rollback-verification.sh

# Check metrics
# Open Grafana: https://grafana.platform-factory.dev

# Check logs
journalctl -u platform-factory-* --since "rollback time" -f
```

### 2. Communication

```bash
# Notify stakeholders (template)
cat <<EOF | mail -s "Rollback Completed: platform-factory v4.0.0 -> v3.2.1" stakeholders@your-org.com
Rollback Summary:
- Previous Version: v4.0.0
- Rollback Version: v3.2.1
- Rollback Time: $(date -u)
- Components Rolled Back:
  * platform-factory CLI
  * platform-factory-control-plane
  * platform-factory-worker
- Status: Success
- Impact: Minimal
- Next Steps: Investigate v4.0.0 issues

Detailed Report: https://wiki.platform-factory.dev/rollbacks/2026-08-02
EOF
```

### 3. Investigation

```bash
# Analyze what went wrong
./scripts/analyze-failure.sh v4.0.0

# Collect logs from failed version
./scripts/collect-failure-logs.sh v4.0.0

# Identify root cause
./scripts/identify-root-cause.sh v4.0.0
```

### 4. Documentation

```bash
# Create rollback report
./scripts/create-rollback-report.sh v4.0.0 v3.2.1

# Update runbook with lessons learned
vim docs/runbooks/rollback.md

# Commit changes
git add .
git commit -m "docs: update rollback procedures based on v4.0.0 rollback"
git push origin main
```

### 5. Planning Next Steps

```bash
# Options after rollback:
# 1. Fix issues in v4.0.0 and re-release as v4.0.1
# 2. Create hot patch for v3.2.1
# 3. Investigate and fix in development branch

# Decision factors:
# - Severity of the issue
# - Time required to fix
# - Impact on users
# - Availability of workarounds

# Create action plan
echo "Action Plan for v4.0.0 Issues" > action-plan.md
echo "=============================" >> action-plan.md
echo "" >> action-plan.md
echo "1. Issue Analysis:" >> action-plan.md
echo "   - [ ] Identify root cause" >> action-plan.md
echo "   - [ ] Reproduce in test environment" >> action-plan.md
echo "" >> action-plan.md
echo "2. Fix Options:" >> action-plan.md
echo "   - [ ] Hot patch for v3.2.1" >> action-plan.md
echo "   - [ ] Fix in v4.0.0 and re-release as v4.0.1" >> action-plan.md
echo "   - [ ] Fix in development for next major version" >> action-plan.md
echo "" >> action-plan.md
echo "3. Testing:" >> action-plan.md
echo "   - [ ] Test fix in isolation" >> action-plan.md
echo "   - [ ] Test rollback and roll-forward" >> action-plan.md
echo "   - [ ] Test in staging environment" >> action-plan.md
echo "" >> action-plan.md
echo "4. Timeline:" >> action-plan.md
echo "   - Target fix date: " >> action-plan.md
echo "   - Target release date: " >> action-plan.md
```

## Rollback Scenarios

### Scenario 1: Build System Failure

**Symptoms**: All builds failing, build queue stuck

**Rollback Procedure**:
1. Rollback control plane to previous version
2. Rollback all workers to previous version
3. Verify build functionality
4. Re-trigger failed builds

**Estimated Time**: 15-30 minutes

### Scenario 2: Registry Incompatibility

**Symptoms**: Push/pull operations failing, authentication errors

**Rollback Procedure**:
1. Rollback control plane
2. Rollback registry client components
3. Clear local cache
4. Retry operations

**Estimated Time**: 10-20 minutes

### Scenario 3: MicroVM Execution Failure

**Symptoms**: MicroVMs failing to start, execution errors

**Rollback Procedure**:
1. Rollback MicroVM manager
2. Rollback hypervisor components
3. Restart affected MicroVMs
4. Verify MicroVM functionality

**Estimated Time**: 20-40 minutes

### Scenario 4: Signing/Verification Failure

**Symptoms**: Signing operations failing, verification errors

**Rollback Procedure**:
1. Rollback signing service
2. Rollback all components that use signing
3. Verify signature validation
4. Re-sign affected artifacts

**Estimated Time**: 15-30 minutes

### Scenario 5: Full System Failure

**Symptoms**: Complete system outage, multiple components failing

**Rollback Procedure**:
1. Rollback all components to previous version
2. Restore state from backup
3. Verify all services
4. Monitor for stability

**Estimated Time**: 45-90 minutes

## Backup and Restore

### Backup Procedures

```bash
# Full system backup
sudo ./scripts/backup-all.sh v4.0.0-$(date +%Y%m%d-%H%M%S)

# Incremental backup
sudo ./scripts/backup-incremental.sh

# Verify backups
sudo ./scripts/verify-backups.sh

# List backups
ls -la /var/backups/platform-factory/
```

### Restore Procedures

```bash
# Full system restore
sudo ./scripts/restore-all.sh v3.2.1

# Partial restore (selective components)
sudo ./scripts/restore-select.sh v3.2.1 control-plane worker-1 worker-2

# Verify restore
sudo ./scripts/verify-restore.sh v3.2.1
```

### Backup Storage

| Backup Type | Retention | Location |
|-------------|-----------|----------|
| Full Backups | 30 days | /var/backups/platform-factory/ |
| Incremental Backups | 7 days | /var/backups/platform-factory/incremental/ |
| State Backups | 365 days | /var/backups/platform-factory/state/ |
| Offsite Backups | Indefinite | S3/Cloud Storage |

### Backup Verification

```bash
# Test backup integrity
./scripts/test-backup.sh /var/backups/platform-factory/v4.0.0-20260802.tar.gz

# Test restore procedure
./scripts/test-restore.sh v3.2.1
```

## Monitoring Rollback

### Key Metrics to Monitor

| Metric | Expected Behavior | Alert Threshold |
|--------|-------------------|-----------------|
| Build Success Rate | Should return to 99.9% | < 99% |
| Push/Pull Success Rate | Should return to 99.99% | < 99.9% |
| MicroVM Startup Time | Should be < 2s | > 5s |
| Signing Success Rate | Should be 100% | < 100% |
| Error Rate | Should decrease to 0 | > 0 |
| Latency | Should return to baseline | 2x baseline |

### Monitoring Commands

```bash
# Watch build success rate
watch -n 5 "./platform-factory metrics get build_success_rate"

# Watch error rate
watch -n 5 "./platform-factory metrics get error_rate"

# Check system health
watch -n 10 "./scripts/health-check.sh"
```

## Communication Templates

### Rollback Announcement (Internal)

```markdown
## 🔙 Rollback in Progress: platform-factory v4.0.0 → v3.2.1

**Status**: In Progress
**Start Time**: 2026-08-02T14:30:00Z
**Expected Completion**: 2026-08-02T15:00:00Z

### Impact
- All platform-factory services will be unavailable during rollback
- Expected downtime: 30 minutes
- Data loss: None expected

### Components Affected
- [x] platform-factory CLI
- [x] platform-factory-control-plane
- [x] platform-factory-worker (all instances)
- [ ] Other components

### Rollback Steps
- [x] Backup current state
- [x] Notify stakeholders
- [ ] Stop services
- [ ] Deploy previous version
- [ ] Start services
- [ ] Verify functionality
- [ ] Announce completion

### Contact
For questions or issues, contact @CYPT71 or security@platform-factory.dev
```

### Rollback Completion (Internal)

```markdown
## ✅ Rollback Complete: platform-factory v4.0.0 → v3.2.1

**Status**: Completed
**Start Time**: 2026-08-02T14:30:00Z
**Completion Time**: 2026-08-02T14:45:00Z
**Duration**: 15 minutes

### Results
- ✅ All services restored to v3.2.1
- ✅ Health checks passing
- ✅ Smoke tests passing
- ✅ No data loss
- ✅ No downtime beyond expected window

### Next Steps
1. Investigate v4.0.0 issues
2. Fix identified problems
3. Plan re-deployment
4. Update documentation

### Lessons Learned
- Issue was caused by [root cause]
- Rollback procedure worked as expected
- [Any improvements needed]

### Contact
For questions, contact @CYPT71 or maintainers@platform-factory.dev
```

### Rollback Announcement (External)

```markdown
# Service Update: platform-factory v4.0.0 Rollback

We have rolled back platform-factory from version 4.0.0 to version 3.2.1 due to [brief
description of issue].

## Impact
- **Services Affected**: Build, Registry, Execution
- **Duration**: 15 minutes
- **Current Status**: All services operational
- **Data Loss**: None

## What Happened

[Brief description of what went wrong]

## What We're Doing

[Description of investigation and fix in progress]

## Timeline

| Time (UTC) | Event |
|------------|-------|
| 14:30 | Issue detected |
| 14:35 | Investigation started |
| 14:40 | Rollback decision made |
| 14:45 | Rollback completed |
| 14:50 | Services verified |

## Next Steps

We are investigating the issue and will provide an update within 24 hours.

For questions or concerns, please contact support@platform-factory.dev.
```

## Tools and Scripts

### Rollback Scripts

| Script | Description |
|--------|-------------|
| `scripts/rollback-cli.sh` | Rollback CLI to previous version |
| `scripts/rollback-all.sh` | Rollback all components |
| `scripts/rollback-control-plane.sh` | Rollback control plane |
| `scripts/rollback-workers.sh` | Rollback all workers |
| `scripts/test-rollback.sh` | Test rollback procedure |
| `scripts/verify-rollback.sh` | Verify rollback success |

### Backup Scripts

| Script | Description |
|--------|-------------|
| `scripts/backup-all.sh` | Backup all components and state |
| `scripts/backup-incremental.sh` | Incremental backup |
| `scripts/backup-state.sh` | Backup state only |
| `scripts/restore-all.sh` | Restore all from backup |
| `scripts/restore-select.sh` | Restore selective components |
| `scripts/verify-backups.sh` | Verify backup integrity |

### Verification Scripts

| Script | Description |
|--------|-------------|
| `scripts/health-check.sh` | Check system health |
| `scripts/smoke-test.sh` | Run smoke tests |
| `scripts/verify-data.sh` | Verify data consistency |
| `scripts/verify-installation.sh` | Verify installation |
| `scripts/verify-version.sh` | Verify version |

## Maintenance

This document should be reviewed and updated:
- After each rollback (add lessons learned)
- When new components are added
- When rollback procedures change
- Quarterly (comprehensive review)

**Last Updated**: 2026-08-02
**Owner**: @CYPT71
**Review Date**: 2026-11-02
