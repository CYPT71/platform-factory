# Registry provider compatibility

Platform Factory speaks the OCI Distribution API directly. Provider entries
below describe configuration at the boundary; they do not replace the native
client and do not imply a provider has passed the real interoperability suite.
The status column is deliberately evidence-based.

| Registry | Current evidence | Provider-specific setup |
| --- | --- | --- |
| OCI Distribution 2.8.3 | Tested locally and continuously configured in `ci-compatibility.yml` | No extension. The CI image is pinned by digest and served over loopback HTTP only with `--insecure-registry`. Production endpoints must use trusted HTTPS. |
| GHCR | Existing Docker/Cosign workflow is green; native `pf publish` provider test still pending | Host is `ghcr.io`; namespace is the GitHub owner. In Actions use `GITHUB_TOKEN` with `packages: write`; outside Actions GitHub documents a classic PAT. A first package is private by default and package/repository access may need linking. Add `org.opencontainers.image.source` when repository association matters. |
| Harbor | Not yet tested by this repository | The Harbor project must exist before push. Prefer a project robot account scoped to pull+push; Harbor does not allow push without pull. OCI subject artifacts, tag immutability, retention, scanning and replication are Harbor policy surfaces and must be tested with the deployed Harbor version. |
| JFrog Artifactory | Not yet tested by this repository | Use the Docker/OCI Registry endpoint shown by “Set Me Up”, not an `/artifactory/...` API URL. Cloud deployments may use repository-path or subdomain routing; self-hosted routing depends on reverse-proxy configuration. Supply username plus access token and use trusted HTTPS. Repository-key/DNS length limits apply to subdomain routing. |
| Amazon ECR | Not yet tested by this repository | The repository must exist and IAM must allow its upload/manifest actions. Resolve the regional endpoint (`ACCOUNT.dkr.ecr.REGION.amazonaws.com`), obtain a short-lived authorization password, use username `AWS`, and refresh it rather than persisting it. Cross-repository mount behavior must be treated as optional. |

Platform Factory credentials are provided as a username flag and password
environment variable so the secret is never placed in argv:

```bash
export PLATFORM_FACTORY_REGISTRY_PASSWORD="..."
pf publish --yes --username "$REGISTRY_USER" \
  oci-image "$REGISTRY_HOST/$REPOSITORY:v1"
```

For a development-only HTTP endpoint, `--insecure-registry` is an explicit
opt-in. It must not be used merely to bypass an invalid production certificate.
The native client accepts Bearer challenges and can send Basic credentials
preemptively; it fails closed on unsupported challenge shapes.

Provider references:

- [GitHub Container Registry](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)
- [Harbor projects](https://goharbor.io/docs/main/working-with-projects/) and [project robot accounts](https://goharbor.io/docs/main/working-with-projects/project-configuration/create-robot-accounts/)
- [JFrog Docker repositories](https://docs.jfrog.com/artifactory/docs/docker-repositories)
- [Amazon ECR private repositories](https://docs.aws.amazon.com/AmazonECR/latest/userguide/Repositories.html)

## Acceptance contract for a new provider

A provider becomes “tested” only when an isolated CI job performs all of the
following against that actual service and retains run-linked evidence:

1. build and strictly verify a fresh OCI layout;
2. publish with the native `pf publish` path using least-privilege credentials;
3. verify the returned manifest digest against the local selected manifest;
4. fetch by digest with an independent OCI client;
5. repeat the push to exercise existing blobs;
6. publish and retrieve OCI subject artifacts when supported;
7. prove a failed evidence/policy step cannot move the tag;
8. remove the disposable repository/tag when provider policy permits cleanup.

Local simulators or mocked HTTP servers remain useful protocol tests, but do
not satisfy a named provider row.
