# Pricing (draft)

> **This is a draft business proposal, not a published offer.** Prices,
> tier boundaries, and what each tier actually gates are all placeholders
> for the maintainer to set deliberately — none of this should be quoted
> to a real prospect, and no feature listed as "Enterprise" below is
> actually gated in code yet unless `internal/license`
> ([package doc](../../internal/license/doc.go)) says otherwise at the
> time you're reading this. Keep this page in sync with what the code
> actually enforces, not the other way around.

See [Licensing](../licensing.md) for what the [LICENSE](../../LICENSE)
itself already grants for free — this page is about what sits on top of
that, not a restatement of it.

## Community (free)

Everything the [Business Source License](../../LICENSE)'s Additional Use
Grant already covers: self-hosted use, internal use, and building your own
product or service on top of Platform Factory for your own customers — at
no cost, with no license key required. This is not a trial; it does not
expire.

**Not included:** the one case BSL carves out (reselling Platform Factory
itself, or a product substantially deriving its value from it, as a
hosted/managed offering competing with Licensor's own) — [TODO: decide
whether that case is ever actually sold, and at what terms, versus simply
refused].

## Enterprise (commercial license)

[TODO: this entire tier is a placeholder shape, not a decided product.
Pick 2-4 things real customers would actually pay for — candidates already
visible in this repository's own roadmap (`todo-next.md`) and
`docs/reference/maturity.md` include: a support SLA with a real response-
time commitment; the explicit right to host Platform Factory as a
service (the case BSL's Additional Use Grant excludes); a seat/instance
count above whatever the Community default ends up being; or priority
access to a specific capability the roadmap marks as still maturing (e.g.
the multi-tenant control-plane, KubeVirt backend). Do not gate a
capability behind a license key just because it is listed here — gate it
because a real customer conversation asked for exactly that split.]

**Price:** [TODO: per-seat, per-instance, flat annual, or usage-based —
undecided]

**How it works today:** `internal/license` can sign and verify a license
file (Licensee, edition, seat/instance limit, expiry, feature list) with
the same Ed25519 primitives `internal/signing` already uses elsewhere in
this project. No command currently reads one to gate a capability at
runtime — wiring that up is the next real step once a specific gated
capability is chosen, not before.

## Support-only add-on

[TODO: a middle tier some BSL-licensed infrastructure projects (MariaDB,
CockroachDB) offer — a paid support/SLA relationship without necessarily
needing a commercial license, since Community use is already unrestricted
for everything except the one BSL carve-out above. Decide whether this is
worth offering separately from Enterprise before writing copy for it.]

## Contact

[TODO: see the licensing contact placeholder in [LICENSE](../../LICENSE).]
