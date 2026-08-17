# Service Level Objectives (SLO)

This document defines the Service Level Objectives (SLOs) for the platform-factory project.
SLOs are measurable targets for system reliability and performance.

## Overview

Our SLOs follow the principle that reliability is a feature. We define clear, measurable
targets for our services and track our performance against these targets.

## Error Budgets

Each SLO has an associated error budget, which is the amount of "unreliable" behavior
we can tolerate before we must stop releasing new features and focus on reliability.

### Error Budget Policy

- **Error Budget**: 100% - SLO%
- **Burn Rate**: Rate at which we consume the error budget
- **Action Required**: When error budget is 50% consumed, we pause feature development
- **Emergency**: When error budget is 90% consumed, all hands focus on reliability

## Build System SLOs

### Build Success Rate

| Metric | Target | Measurement Window | Error Budget |
|--------|--------|---------------------|---------------|
| Build Success Rate | 99.9% | 30 days | 0.1% |

**Definition**: Percentage of builds that complete successfully without errors.

**Measurement**: Tracked per repository and aggregated across all builds.

**Exclusions**:
- Builds that fail due to user configuration errors
- Builds that are explicitly cancelled by users
- Test failures (tracked separately)

### Build Time

| Metric | Target | Measurement Window |
|--------|--------|---------------------|
| P99 Build Time | < 10 minutes | 7 days |
| P95 Build Time | < 5 minutes | 7 days |
| P50 Build Time | < 2 minutes | 7 days |

**Definition**: Time from build initiation to successful completion.

**Measurement**: Measured for each build stage separately and for the total build.

### Build Reproducibility

| Metric | Target | Measurement Window |
|--------|--------|---------------------|
| Reproducibility Rate | 100% | All builds |

**Definition**: Percentage of builds that produce identical outputs given identical inputs.

**Measurement**: Verified by comparing digests of build outputs.

## Registry SLOs

### Push Success Rate

| Metric | Target | Measurement Window | Error Budget |
|--------|--------|---------------------|---------------|
| Push Success Rate | 99.95% | 30 days | 0.05% |

**Definition**: Percentage of image push operations that complete successfully.

**Exclusions**:
- Pushes that fail due to network connectivity issues (client side)
- Pushes rejected due to quota limits
- Pushes cancelled by users

### Pull Success Rate

| Metric | Target | Measurement Window | Error Budget |
|--------|--------|---------------------|---------------|
| Pull Success Rate | 99.99% | 30 days | 0.01% |

**Definition**: Percentage of image pull operations that complete successfully.

### Pull Latency

| Metric | Target | Measurement Window |
|--------|--------|---------------------|
| P99 Pull Latency | < 500ms | 7 days |
| P95 Pull Latency | < 100ms | 7 days |
| P50 Pull Latency | < 50ms | 7 days |

**Definition**: Time from pull request to first byte of layer data.

## Execution SLOs

### MicroVM Startup Time

| Metric | Target | Measurement Window |
|--------|--------|---------------------|
| P99 Startup Time | < 2 seconds | 7 days |
| P95 Startup Time | < 1 second | 7 days |
| P50 Startup Time | < 500ms | 7 days |

**Definition**: Time from `create` request to MicroVM being ready to accept commands.

### MicroVM Uptime

| Metric | Target | Measurement Window |
|--------|--------|---------------------|
| Crash-Free Rate | 99.99% | 30 days | 0.01% |

**Definition**: Percentage of MicroVM instances that do not crash during their lifetime.

**Exclusions**:
- MicroVMs intentionally stopped by users
- MicroVMs terminated due to resource exhaustion (OOM, etc.)
- MicroVMs terminated due to host shutdown

## Plugin System SLOs

### Plugin Load Success Rate

| Metric | Target | Measurement Window | Error Budget |
|--------|--------|---------------------|---------------|
| Plugin Load Success Rate | 99.9% | 30 days | 0.1% |

**Definition**: Percentage of plugin load operations that complete successfully.

### Plugin Execution Time

| Metric | Target | Measurement Window |
|--------|--------|---------------------|
| P99 Plugin Time | < 1 second | 7 days |
| P95 Plugin Time | < 100ms | 7 days |

**Definition**: Time from plugin invocation to completion.

## Signing SLOs

### Signing Success Rate

| Metric | Target | Measurement Window | Error Budget |
|--------|--------|---------------------|---------------|
| Signing Success Rate | 100% | 30 days | 0% |

**Definition**: Percentage of signing operations that complete successfully.

**Rationale**: Signing failures can prevent deployment. We must ensure 100% reliability.

### Verification Success Rate

| Metric | Target | Measurement Window | Error Budget |
|--------|--------|---------------------|---------------|
| Verification Success Rate | 100% | 30 days | 0% |

**Definition**: Percentage of signature verification operations that complete successfully.

## Monitoring and Alerting

### Monitoring Stack

- **Metrics Collection**: Prometheus
- **Visualization**: Grafana
- **Alerting**: Alertmanager
- **Tracing**: OpenTelemetry

### Alert Rules

| Alert | Condition | Severity | Response Time |
|-------|-----------|----------|---------------|
| Build Failure Rate > 1% | 5-minute window | Critical | 5 minutes |
| Push Failure Rate > 0.1% | 5-minute window | Critical | 5 minutes |
| Pull Failure Rate > 0.01% | 5-minute window | Critical | 5 minutes |
| Pull Latency P99 > 1s | 5-minute window | High | 15 minutes |
| MicroVM Crash Rate > 0.01% | 5-minute window | Critical | 5 minutes |
| Plugin Load Failure Rate > 0.1% | 5-minute window | High | 15 minutes |
| Signing Failure | Any occurrence | Critical | Immediate |
| Verification Failure | Any occurrence | Critical | Immediate |

### Dashboards

1. **Build System Dashboard**: Build success rate, build times, reproducibility
2. **Registry Dashboard**: Push/pull success rates, latencies, throughput
3. **Execution Dashboard**: MicroVM startup times, uptime, crash rates
4. **Plugin Dashboard**: Load success rates, execution times
5. **Signing Dashboard**: Signing/verification success rates, latencies

## Incident Response

### Incident Classification

| Severity | Description | Response Time | Resolution Time |
|----------|-------------|---------------|-----------------|
| SEV-1 (Critical) | Complete service outage, security vulnerability | Immediate | 1 hour |
| SEV-2 (High) | Significant service degradation, partial outage | 15 minutes | 4 hours |
| SEV-3 (Medium) | Minor service degradation, non-critical features | 1 hour | 24 hours |
| SEV-4 (Low) | Cosmetic issues, minor bugs | 4 hours | 72 hours |

### Incident Response Team

- **Primary On-Call**: Cyprien (@CYPT71)
- **Secondary On-Call**: TBD
- **Escalation Path**: security@platform-factory.dev -> maintainers@platform-factory.dev

### Incident Response Process

1. **Detection**: Automated alerting or user report
2. **Triage**: Initial assessment within response time
3. **Diagnosis**: Identify root cause
4. **Mitigation**: Implement temporary fix or workaround
5. **Resolution**: Permanent fix
6. **Post-Mortem**: Document incident within 48 hours

## Post-Mortem Process

### Post-Mortem Template

```markdown
# Incident Post-Mortem: [Incident Name]

**Date**: YYYY-MM-DD
**Time**: Start - End (UTC)
**Severity**: SEV-X
**Status**: Resolved

## Summary

Brief description of the incident.

## Timeline

- [HH:MM UTC] Event 1
- [HH:MM UTC] Event 2
- [HH:MM UTC] Event 3

## Impact

- Affected services: List of services
- User impact: Description of user impact
- Duration: Total duration of impact

## Root Cause

Detailed explanation of the root cause.

## Detection

How the incident was detected.

## Response

Actions taken during the incident.

## Resolution

How the incident was resolved.

## Lessons Learned

- What went well
- What could be improved
- Action items

## Action Items

- [ ] Action item 1 (Owner, Due Date)
- [ ] Action item 2 (Owner, Due Date)
```

### Post-Mortem Meeting

- Scheduled within 48 hours of resolution
- Attendees: Incident responders, relevant team members
- Duration: 1 hour
- Focus: Review timeline, identify improvements, assign action items

## Testing

### SLO Testing

SLOs are tested through:
1. **Synthetic Monitoring**: Automated tests that verify SLO compliance
2. **Load Testing**: Testing under load to verify performance targets
3. **Chaos Engineering**: Proactively testing failure scenarios

### SLO Review

SLOs are reviewed quarterly to:
- Assess if targets are still appropriate
- Update targets based on user feedback and business needs
- Identify areas for improvement

## Documentation

- **SLO Dashboard**: Real-time SLO compliance dashboard
- **Error Budget Tracking**: Visualization of error budget consumption
- **Incident Log**: Historical record of all incidents
- **Post-Mortem Archive**: All post-mortem documents

## Contact

- **Incident Reporting**: security@platform-factory.dev
- **SLO Questions**: maintainers@platform-factory.dev
- **On-Call**: [On-call schedule link]

## Version History

| Version | Date | Changes |
|---------|------|---------|
| 1.0 | 2026-08-02 | Initial SLO definitions |
