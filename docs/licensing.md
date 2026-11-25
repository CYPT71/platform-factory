# Licensing

Platform Factory is licensed under the [Business Source License 1.1](../LICENSE)
("BSL"), effective for this version and every version released after it.
Versions released before this change remain available under the MIT License
terms they were originally published under — this page does not reach
backward and does not ask anyone to re-license code they already have.

This page explains the license in plain language. The [LICENSE](../LICENSE)
file itself is the actual legal text and controls in any conflict with this
page.

## What BSL means here

BSL is a source-available license, not an OSI-approved open source license.
The source stays public and most uses stay free; the license adds one
specific restriction (the "Additional Use Grant" in [LICENSE](../LICENSE))
and a promise:

- **Free for almost everything.** Running Platform Factory yourself — in
  development, in CI, in production, internally, or to build and operate
  your own product or service for your own customers — costs nothing and
  needs no permission, under the Additional Use Grant.
- **One restriction.** What the Additional Use Grant does not cover:
  reselling Platform Factory itself (or a product whose value is
  substantially Platform Factory's own functionality) as a hosted or
  managed offering that competes with Licensor's own commercial offering of
  it. That specific case needs a commercial license — see
  [Pricing](legal/pricing.md) and the contact address in [LICENSE](../LICENSE).
- **A deadline on the restriction, not the software.** On the Change Date in
  [LICENSE](../LICENSE) (2030-11-25, or the fourth anniversary of a given
  version's first release, whichever comes first, per BSL's own terms), that
  version automatically becomes available under the MIT License instead —
  permanently, for that version. Every later version carries the same
  four-year clock forward.

## Why

Platform Factory's actual engineering (the thing this repository's own
`todo-next.md` and CI evidence are about) stays exactly as open and
auditable as it already was: every line of source, every test, every piece
of evidence this project has built its credibility on is still public from
day one, not withheld until the Change Date. What changes is a single
downstream case — a third party standing up "Platform Factory as a service"
to compete directly with the project's own commercial offering before the
project has had a chance to build one. BSL is the license several other
infrastructure projects (MariaDB, Sentry, CockroachDB, HashiCorp's past
releases, among others) have used for exactly this reason, which is also why
its text is reusable: MariaDB's own covenant in [LICENSE](../LICENSE)
explicitly permits any licensor to reuse the BSL template this way.

## Commercial licensing and license keys

Some capabilities build on top of BSL rather than inside it: a signed
license (see `internal/license`, [package doc](../internal/license/doc.go))
that the Enterprise features check, resellers who want a BSL exception, and
the usual OEM/embed conversations BSL itself invites ("purchase a commercial
license from the Licensor"). None of that affects what is already public
and free to self-host under the Additional Use Grant above — it exists for
the specific cases that grant does not cover, or for customers who want a
support/SLA relationship rather than just license permission.

## This is a draft of the business decision, not just the wording

The Additional Use Grant text, the Change Date, and the Change License in
[LICENSE](../LICENSE) are the real legal parameters — this page just
explains them. They were set by this project's maintainer as a reasonable
starting point, not run past outside counsel. Treat the exact wording as a
draft until it has had a real legal review in the jurisdiction(s) that
matter for this business, the same caveat [the Terms of Service](legal/terms-of-service.md)
and [Privacy Policy](legal/privacy-policy.md) carry.
