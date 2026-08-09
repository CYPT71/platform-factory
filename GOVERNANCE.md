# Project Governance

This document describes the governance model for the secure-oci project.

## Overview

The secure-oci project is a secure, production-ready OCI (Open Container Initiative) implementation
that provides deterministic builds, cryptographic provenance, and hardware-rooted trust for container
workloads.

## Roles and Responsibilities

### Maintainers

Maintainers are individuals with write access to the repository who are responsible for:
- Reviewing and merging pull requests
- Triaging issues and bug reports
- Releasing new versions
- Enforcing project policies and standards
- Maintaining CI/CD infrastructure

### Crypto Team

The cryptography team is responsible for:
- All cryptographic primitives and implementations
- Key management and signing infrastructure
- TLS/mTLS configuration
- Security audits of cryptographic code

### Registry Team

The registry team is responsible for:
- Registry client implementation
- Image distribution and caching
- OCI spec compliance
- Registry security

### MicroVM Team

The MicroVM team is responsible for:
- Virtualization infrastructure (KVM, HVF)
- MicroVM execution environment
- Hypervisor integration
- Secure isolation mechanisms

### DevOps Team

The DevOps team is responsible for:
- CI/CD pipelines
- GitHub Actions workflows
- Release automation
- Infrastructure as code

## Decision Making

### Consensus Seeking

Most decisions are made through consensus among maintainers. The process is:

1. **Proposal**: A maintainer or contributor proposes a change or new feature
2. **Discussion**: The community discusses the proposal on GitHub issues or discussions
3. **Feedback**: Maintainers and community members provide feedback
4. **Consensus**: The proposal is accepted when there is general agreement
5. **Implementation**: The change is implemented and reviewed

### Formal Votes

For contentious decisions, a formal vote may be called. Votes are open to all maintainers
and require a simple majority to pass. In case of a tie, the project lead has the casting vote.

### Security Decisions

Security-related decisions require approval from at least two maintainers with security
expertise, one of whom must be from the crypto team if the decision affects cryptographic
components.

## Contribution Guidelines

See [CONTRIBUTING.md](CONTRIBUTING.md) for detailed contribution guidelines.

## Release Process

### Release Cadence

The project follows a semantic versioning (SemVer) approach:
- **Major versions (vX.0.0)**: Breaking changes, new features, architectural changes
- **Minor versions (vX.Y.0)**: Backward-compatible new features
- **Patch versions (vX.Y.Z)**: Bug fixes and security patches

### Release Approval

1. A release candidate is created by a maintainer
2. The release candidate is tested by the community
3. At least two maintainers must approve the release
4. The release is published with signed artifacts
5. Release notes are published

### Emergency Releases

For critical security vulnerabilities, an emergency release process is followed:
1. The vulnerability is reported to the security team
2. A fix is developed and reviewed in private
3. A patch release is prepared
4. The fix is published simultaneously with a security advisory
5. Users are notified through security channels

## Security Policy

### Reporting Vulnerabilities

Security vulnerabilities should be reported privately to the security team at
`security@secure-oci.dev` (example email - replace with actual contact).

Do not report security vulnerabilities through public GitHub issues.

### Vulnerability Handling

1. **Triage**: The security team triages the report within 24 hours
2. **Assessment**: The severity and impact are assessed
3. **Fix**: A fix is developed in a private repository
4. **Review**: The fix is reviewed by the security team
5. **Disclosure**: The fix is disclosed according to the severity:
   - Critical: Immediate disclosure with fix
   - High: Disclosure within 7 days
   - Medium: Disclosure within 30 days
   - Low: Disclosure within 90 days

### Security Advisories

All security vulnerabilities that are fixed are documented in security advisories
published in the [security directory](.github/security).

## Code of Conduct

See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) for our code of conduct.

## Maintainer Responsibilities

Maintainers must:
- Be responsive to issues and pull requests
- Follow the code of conduct
- Maintain the security and integrity of the project
- Disclose conflicts of interest
- Respect confidentiality of private discussions

## Maintainer Removal

Maintainers who are no longer active or contributing to the project may be removed
as maintainers after a period of inactivity (typically 6 months). The decision to
remove a maintainer requires consensus among the remaining maintainers.

## Amendments

This governance document may be amended with consensus among maintainers.
Amendments must be documented as changes to this file.
