# Incident Response Runbook

This document provides step-by-step procedures for responding to incidents in the
platform-factory project.

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
# 1. Acknowledge the alert
#    - In Alertmanager: Acknowledge the alert
#    - In PagerDuty: Acknowledge the page
#    - In GitHub: Comment on the issue

# 2. Start the incident timer
INCIDENT_START=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# 3. Create an incident channel (if using Slack/Discord)
#    - Name: incident-<type>-<date>
#    - Example: incident-build-2026-08-02

# 4. Identify the incident commander
#    - Primary on-call or most available maintainer

# 5. Begin investigation
```

### 2. Investigation

```bash
# Check system status
./scripts/health-check.sh

# Check recent logs
journalctl -u platform-factory-* --since "1 hour ago" -f

# Check metrics dashboard
# Open Grafana dashboard: https://grafana.platform-factory.dev

# Check recent deployments
git log --oneline --since="24 hours ago"

# Check CI/CD status
# Open GitHub Actions: https://github.com/CYPT71/platform-factory/actions
```

### 3. Mitigation

```bash
# If the issue is identified and can be fixed quickly:
# 1. Implement the fix
# 2. Verify the fix resolves the issue
# 3. Monitor for recurrence

# If the issue requires more investigation:
# 1. Implement a temporary workaround if available
# 2. Continue investigation
# 3. Escalate if needed
```

### 4. Resolution

```bash
# Verify the fix is working
./scripts/verify-fix.sh

# Monitor for at least 30 minutes
watch -n 30 ./scripts/health-check.sh

# Document the resolution
INCIDENT_END=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
DURATION=$(( $(date -d "$INCIDENT_END" +%s) - $(date -d "$INCIDENT_START" +%s) ))
echo "Incident duration: $DURATION seconds"
```

### 5. Post-Incident

```bash
# 1. Schedule post-mortem meeting
# 2. Write incident report (see template in SLO.md)
# 3. Identify and assign action items
# 4. Update runbooks if needed
```

---

## Build System Incidents

### Build Failures

**Symptoms:**
- Builds failing consistently
- Increased build failure rate
- Specific stages failing

**Immediate Actions:**

```bash
# Check build logs
cat /var/log/platform-factory/build.log | tail -100

# Check for specific error patterns
grep -i "error\|fail\|panic" /var/log/platform-factory/build.log | tail -50

# Check disk space
df -h /var/lib/platform-factory

# Check memory usage
free -h

# Check for resource exhaustion
lsof | wc -l
```

**Common Causes and Solutions:**

1. **Disk Space Exhaustion**
   ```bash
   # Check disk usage
   du -sh /var/lib/platform-factory/* | sort -h
   
   # Clean up old build artifacts
   find /var/lib/platform-factory/cache -mtime +7 -delete
   
   # Clean up old logs
   find /var/log/platform-factory -name "*.log" -mtime +7 -delete
   ```

2. **Memory Exhaustion**
   ```bash
   # Check memory usage by process
   ps aux --sort=-%mem | head -20
   
   # Increase memory limits (temporary)
   # Edit systemd service file
   sudo systemctl edit platform-factory-builder
   # Add: MemoryLimit=8G
   sudo systemctl daemon-reload
   sudo systemctl restart platform-factory-builder
   ```

3. **Network Issues**
   ```bash
   # Check network connectivity
   ping google.com
   curl -v https://registry-1.docker.io
   
   # Check DNS resolution
   nslookup registry-1.docker.io
   dig registry-1.docker.io
   
   # Check proxy settings
   env | grep -i proxy
   ```

4. **Dependency Issues**
   ```bash
   # Check if dependencies are available
   go mod download
   
   # Check Go toolchain
   go version
   
   # Verify GOTOOLCHAIN
   echo $GOTOOLCHAIN
   ```

### Slow Builds

**Symptoms:**
- Builds taking longer than SLO targets
- Specific stages are slow

**Immediate Actions:**

```bash
# Profile build times
./platform-factory build --profile=build.prof project.yaml

# Analyze profile
go tool pprof build.prof

# Check resource usage during build
top -c
htop
```

**Common Causes and Solutions:**

1. **Inefficient Build Steps**
   ```bash
   # Identify slow stages
   ./platform-factory build --timing project.yaml
   
   # Optimize Dockerfile
   # - Use multi-stage builds
   # - Leverage build cache
   # - Minimize layers
   ```

2. **Network Latency**
   ```bash
   # Test download speeds
   time curl -o /dev/null https://example.com/large-file
   
   # Use local mirrors if available
   # Configure in .config/platform-factory/config.yaml
   ```

3. **Resource Contention**
   ```bash
   # Limit concurrent builds
   ./platform-factory build --parallelism=2 project.yaml
   
   # Check CPU throttling
   cat /proc/cpuinfo | grep -i throttl
   ```

4. **Cache Misses**
   ```bash
   # Check cache hit rate
   ./platform-factory cache stats
   
   # Force cache rebuild if needed
   ./platform-factory cache rebuild
   ```

---

## Registry Incidents

### Push Failures

**Symptoms:**
- Images failing to push
- Push timeouts
- Authentication errors

**Immediate Actions:**

```bash
# Check registry connectivity
curl -v https://ghcr.io

# Check authentication
cat ~/.config/platform-factory/registry-auth.json

# Check network connectivity to registry
nc -zv ghcr.io 443

# Check for rate limiting
curl -I https://ghcr.io
```

**Common Causes and Solutions:**

1. **Authentication Issues**
   ```bash
   # Re-authenticate
   ./platform-factory registry login ghcr.io
   
   # Check token validity
   ./platform-factory registry check-token ghcr.io
   
   # Renew token if expired
   ./platform-factory registry refresh-token ghcr.io
   ```

2. **Network Connectivity**
   ```bash
   # Check firewall rules
   sudo iptables -L -n
   
   # Check for corporate proxy
   env | grep -i proxy
   
   # Temporarily disable firewall for testing
   sudo iptables -F
   ```

3. **Registry Rate Limiting**
   ```bash
   # Check rate limit headers
   curl -I https://ghcr.io | grep -i rate
   
   # Wait and retry
   sleep 60
   ./platform-factory push image:tag
   
   # Use different registry if available
   ./platform-factory push --registry=custom.registry.io image:tag
   ```

4. **Large Image Issues**
   ```bash
   # Check image size
   ./platform-factory image inspect image:tag | grep Size
   
   # Push in smaller chunks
   ./platform-factory push --chunk-size=10M image:tag
   
   # Compress image before push
   ./platform-factory image optimize image:tag
   ```

### Pull Failures

**Symptoms:**
- Images failing to pull
- Pull timeouts
- Digest mismatches

**Immediate Actions:**

```bash
# Check image exists
./platform-factory registry catalog ghcr.io/owner

# Check image manifest
./platform-factory manifest inspect ghcr.io/owner/repo:tag

# Check local cache
ls -la ~/.cache/platform-factory/blobs/sha256/
```

**Common Causes and Solutions:**

1. **Image Not Found**
   ```bash
   # Verify image exists
   ./platform-factory registry catalog ghcr.io/owner
   
   # Check for typos in image name
   # Retry with correct name
   ```

2. **Digest Mismatch**
   ```bash
   # Verify image integrity
   ./platform-factory image verify image:tag
   
   # Re-pull with force
   ./platform-factory pull --force image:tag
   
   # Clean local cache
   ./platform-factory cache clean
   ```

3. **Network Issues**
   ```bash
   # Same troubleshooting as push failures
   ```

4. **Storage Issues**
   ```bash
   # Check local storage
   df -h ~/.cache/platform-factory
   
   # Clean old blobs
   ./platform-factory cache gc
   ```

---

## Execution Incidents

### MicroVM Failures

**Symptoms:**
- MicroVMs failing to start
- MicroVMs crashing
- Execution errors

**Immediate Actions:**

```bash
# Check MicroVM logs
journalctl -u platform-factory-microvm -f

# Check for KVM/HVF support
lsmod | grep kvm
kextstat | grep hvf

# Check virtualization support
virt-host-validate

# Check for nested virtualization
grep -E 'vmx|svm' /proc/cpuinfo
```

**Common Causes and Solutions:**

1. **Virtualization Not Enabled**
   ```bash
   # Check BIOS settings
   sudo dmidecode | grep -i virtual
   
   # Enable virtualization in BIOS/UEFI
   # Reboot required
   ```

2. **KVM Not Available (Linux)**
   ```bash
   # Check KVM modules
   lsmod | grep kvm
   
   # Load KVM modules
   sudo modprobe kvm
   sudo modprobe kvm_intel  # or kvm_amd
   
   # Install KVM packages
   sudo apt-get install qemu-kvm libvirt-daemon-system
   ```

3. **HVF Not Available (macOS)**
   ```bash
   # Check HVF kernel extension
   kextstat | grep hvf
   
   # Enable HVF
   sudo kextload /Library/Extensions/HVF.kext
   
   # Check for macOS version compatibility
   sw_vers
   ```

4. **Resource Issues**
   ```bash
   # Check available memory
   free -h
   
   # Reduce MicroVM memory allocation
   ./platform-factory run --memory=2G image:tag
   ```

### MicroVM Performance Issues

**Symptoms:**
- Slow MicroVM startup
- High CPU usage
- Memory pressure

**Immediate Actions:**

```bash
# Check MicroVM resource usage
./platform-factory microvm ps
./platform-factory microvm stats <id>

# Check host resource usage
top -c
htop

# Check for CPU throttling
mpstat -P ALL 1
```

**Common Causes and Solutions:**

1. **CPU Contention**
   ```bash
   # Limit CPU usage
   ./platform-factory run --cpus=2 image:tag
   
   # Use CPU pinning
   ./platform-factory run --cpus=0-1 image:tag
   ```

2. **Memory Pressure**
   ```bash
   # Check memory usage
   ./platform-factory microvm memory <id>
   
   # Increase memory
   ./platform-factory run --memory=4G image:tag
   ```

3. **Disk I/O Bottleneck**
   ```bash
   # Use faster storage
   ./platform-factory run --storage=/mnt/ssd image:tag
   
   # Check disk performance
   iostat -x 1
   ```

---

## Signing Incidents

### Signing Failures

**Symptoms:**
- Signing operations failing
- Signature verification failures
- Key-related errors

**Immediate Actions:**

```bash
# Check signing logs
journalctl -u platform-factory-signer -f

# Check key availability
ls -la ~/.config/platform-factory/keys/

# Check key permissions
ls -la ~/.config/platform-factory/keys/ | grep -v "drwx"
```

**Common Causes and Solutions:**

1. **Missing Keys**
   ```bash
   # Generate new key
   ./platform-factory key generate my-key
   
   # Import existing key
   ./platform-factory key import my-key.pem
   
   # Check default key
   ./platform-factory config get signing.key
   ```

2. **Key Permissions**
   ```bash
   # Fix permissions
   chmod 600 ~/.config/platform-factory/keys/*.pem
   
   # Check directory permissions
   chmod 700 ~/.config/platform-factory/keys
   ```

3. **Key Corruption**
   ```bash
   # Verify key integrity
   ./platform-factory key verify my-key
   
   # Backup and remove corrupted key
   mv ~/.config/platform-factory/keys/my-key.pem{,.bak}
   
   # Generate new key
   ./platform-factory key generate my-key
   ```

4. **Algorithm Not Supported**
   ```bash
   # Check supported algorithms
   ./platform-factory key algorithms
   
   # Use supported algorithm
   ./platform-factory key generate --algorithm=ed25519 my-key
   ```

### Verification Failures

**Symptoms:**
- Signature verification failing
- Images rejected due to invalid signatures

**Immediate Actions:**

```bash
# Verify specific image
./platform-factory image verify image:tag

# Check image signature
./platform-factory image inspect --show-signature image:tag

# Check public key
./platform-factory key show my-key --public
```

**Common Causes and Solutions:**

1. **Image Modified After Signing**
   ```bash
   # Re-sign the image
   ./platform-factory image sign image:tag
   
   # Verify before pushing
   ./platform-factory image verify --strict image:tag
   ```

2. **Wrong Public Key**
   ```bash
   # Check expected public key
   ./platform-factory trust show
   
   # Add correct public key
   ./platform-factory trust add owner-key.pem
   ```

3. **Signature Expired**
   ```bash
   # Check signature timestamp
   ./platform-factory image inspect --show-signature image:tag | grep Timestamp
   
   # Re-sign with current timestamp
   ./platform-factory image sign --force image:tag
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
# 1. DO NOT discuss in public channels
# 2. Create a private incident channel
# 3. Limit access to incident responders

# Acknowledge the report (if external)
# "Thank you for your report. We are investigating and will respond shortly."

# Begin investigation in private
```

**Response Procedure:**

1. **Triage** (Within 1 hour)
   - Assess the severity and impact
   - Determine if it's a valid vulnerability
   - Identify affected components

2. **Containment** (Within 2 hours)
   - If exploit is ongoing, contain it
   - Revoke compromised credentials
   - Isolate affected systems

3. **Remediation** (Within 24 hours for critical)
   - Develop a fix
   - Test the fix thoroughly
   - Prepare the fix for release

4. **Disclosure** (According to severity)
   - Coordinate with reporter (if external)
   - Prepare security advisory
   - Release fix and advisory simultaneously

**Contact:**
- **Security Team**: security@platform-factory.dev
- **On-Call**: [On-call contact]

---

## Escalation Path

If you cannot resolve an incident:

1. **First Level**: Contact the on-call maintainer
2. **Second Level**: Contact the project lead (Cyprien @CYPT71)
3. **Third Level**: Contact all maintainers via maintainers@platform-factory.dev
4. **Fourth Level**: For critical security issues, contact security@platform-factory.dev

---

## Tools and Commands Reference

### Health Checks

```bash
# Full health check
./scripts/health-check.sh

# Build system health
./scripts/health-check.sh build

# Registry health
./scripts/health-check.sh registry

# Execution health
./scripts/health-check.sh execution

# Signing health
./scripts/health-check.sh signing
```

### Log Collection

```bash
# Collect all logs
./scripts/collect-logs.sh

# Collect build logs
./scripts/collect-logs.sh build

# Collect registry logs
./scripts/collect-logs.sh registry

# Collect MicroVM logs
./scripts/collect-logs.sh microvm
```

### Metrics

```bash
# View Prometheus metrics
curl http://localhost:9090/metrics

# Query specific metric
curl -G http://localhost:9090/api/v1/query --data-urlencode 'query=build_success_rate'
```

---

## Runbook Maintenance

This runbook should be updated:
- After each incident (add lessons learned)
- When new features are added (add new incident types)
- When procedures change
- Quarterly (review and update)

**Last Updated**: 2026-08-02
**Owner**: @CYPT71
