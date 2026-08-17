# Security policy

## Supported versions

Security fixes are provided for the latest stable semantic-version release and
the current `main` branch. Older releases and explicitly experimental runtime
profiles are unsupported unless a release notice states otherwise.

| Version | Security support |
|---|---|
| Latest `vMAJOR.MINOR.PATCH` | Yes |
| Current `main` | Best effort until released |
| Older releases | No |
| Experimental profiles | No compatibility or response-time guarantee |

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Use GitHub's private
security advisory reporting for `CYPT71/platform-factory`. Include the affected
commit or release, reproduction steps, impact, and any suggested mitigation.

Receipt is acknowledged within three business days. An initial assessment is
targeted within seven business days. Confirmed issues are coordinated
privately until a fix and advisory are ready. These targets are operational
goals, not contractual service-level guarantees.

## Disclosure and release

Fixes follow coordinated disclosure. Release artifacts are validated before
publication, published by immutable digest, supplied with SBOM and provenance,
and signed keylessly through GitHub Actions. A release is not considered
supported when its required checks did not pass on the tagged commit.

## Signing identity and rotation

Production releases use short-lived Fulcio certificates issued for the
`ci-release.yml` GitHub Actions OIDC identity; there is no long-lived private
Cosign key to rotate. Rotation consists of reviewing trusted workflow
identities, repository access, environment reviewers and GitHub token
permissions. Any identity or workflow compromise requires revoking access,
disabling publication, repairing the workflow through review, and issuing a
new release digest rather than mutating an existing release.
