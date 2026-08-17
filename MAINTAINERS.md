# Project Maintainers

This document lists the current maintainers of the platform-factory project and their areas
of responsibility.

## Active Maintainers

| Name | GitHub Username | Role | Areas of Responsibility | Contact |
|------|----------------|------|------------------------|---------|
| Cyprien | @CYPT71 | Project Lead | Overall project, architecture, release management | cyprien@platform-factory.dev |

## Teams

### Security & Cryptography Team
- **Lead**: Cyprien (@CYPT71)
- **Responsibilities**:
  - Cryptographic primitives (Ed25519, ECDSA)
  - Signing and verification infrastructure
  - TLS/mTLS implementation
  - Key management
  - Security audits
- **Contact**: security@platform-factory.dev

### Registry Team
- **Lead**: Cyprien (@CYPT71)
- **Responsibilities**:
  - Registry client implementation
  - OCI specification compliance
  - Image distribution and caching
  - Registry security

### MicroVM Team
- **Lead**: Cyprien (@CYPT71)
- **Responsibilities**:
  - Virtualization (KVM, HVF)
  - MicroVM execution environment
  - Hypervisor integration
  - Secure isolation

### DevOps & CI/CD Team
- **Lead**: Cyprien (@CYPT71)
- **Responsibilities**:
  - GitHub Actions workflows
  - CI/CD pipelines
  - Release automation
  - Infrastructure as code

## Former Maintainers

None at this time.

## Becoming a Maintainer

To become a maintainer, an individual must:

1. **Demonstrate Commitment**: Show sustained, high-quality contributions over a
   period of time (typically 3-6 months)

2. **Technical Expertise**: Demonstrate deep understanding of the codebase and
   the problem domain

3. **Review Skills**: Consistently provide thorough, constructive code reviews

4. **Community Engagement**: Be active in discussions, help other contributors,
   and triage issues

5. **Sponsorship**: Be nominated by an existing maintainer

6. **Approval**: Gain consensus approval from existing maintainers

### Maintainer Onboarding

New maintainers receive:
- Write access to the repository
- Invitation to maintainer discussions
- Documentation on maintainer responsibilities
- Mentorship from existing maintainers

### Maintainer Expectations

Maintainers are expected to:
- Review and merge pull requests in a timely manner
- Respond to issues and bug reports
- Participate in release planning and management
- Follow and enforce project policies
- Maintain the security and integrity of the project
- Be available for emergency security responses

## Maintainer Responsibilities by Area

### Core Components

| Component | Maintainer | Backup |
|-----------|------------|--------|
| Pipeline | Cyprien | - |
| Executor | Cyprien | - |
| Sandbox | Cyprien | - |
| Signing | Cyprien | - |
| mTLS | Cyprien | - |

### Infrastructure

| Component | Maintainer | Backup |
|-----------|------------|--------|
| Registry Client | Cyprien | - |
| OCI Build | Cyprien | - |
| CAS (Content Addressable Storage) | Cyprien | - |

### Platform-Specific

| Platform | Maintainer | Backup |
|----------|------------|--------|
| Linux (KVM) | Cyprien | - |
| macOS (HVF) | Cyprien | - |
| Windows | TBD | - |

## Rotation and Succession

Maintainers who are no longer able to fulfill their responsibilities should:
1. Notify the project lead
2. Work with the team to find a replacement
3. Ensure a smooth transition of responsibilities

## Contact

For general inquiries: info@platform-factory.dev
For security issues: security@platform-factory.dev

## Meeting Schedule

- **Weekly Sync**: Every Monday at 10:00 AM UTC
- **Release Planning**: First Monday of each month
- **Security Review**: Second Monday of each month

All meetings are held virtually and recorded for maintainers who cannot attend.
