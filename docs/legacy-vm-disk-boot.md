# Legacy VM disk boot — roadmap

Tracks the "import an old VM disk" slice of Meine-Graal's v6.2 vision
(`.wiki-worktree/Meine-Graal.md`, private wiki submodule — not this
repo's own roadmap, but the source this work is scoped from). This file
is the concrete, testable plan for that one slice; Meine-Graal stays the
long-range vision document.

## What "done" looks like here

A legacy VM disk (an old QCOW2/VMDK/VHD/VHDX/RAW/ISO image nobody has the
original install media or Dockerfile for anymore) boots under
`platform-factory microvm run-legacy-disk`, executing **the disk's own
bootloader and kernel** via QEMU's BIOS/OVMF firmware — not converted,
not re-packaged, not requiring platform-factory's own kernel. A project
can span **multiple disks** (an OS disk plus one or more data disks),
with the boot disk identified automatically wherever that can be done
safely.

## Trust model change — read this before extending this path

Every other MicroVM boot path in this project (native KVM, native HVF,
and the QEMU fallback in `run-microvm.sh`) boots **only this project's
own kernel and initramfs**, built from source in the same run
(`build-kernel.sh`, `cmd/microvm-init`). The guest kernel is trusted
because this project built it.

`run-legacy-disk.sh` breaks that invariant on purpose: it executes
whatever bootloader and kernel are already on the disk, which is
attacker-or-just-unknown-controlled content, not something this project
vetted. Treat this boot mode as a weaker sandbox than every other one in
the repo until it has its own hardening review. Concretely, today it:

- **Never opens any source disk for writing**, boot or secondary. Every
  format except ISO gets a disposable `qemu-img` qcow2 overlay
  (`backing_file` = the source, deleted on exit); ISO is attached as
  read-only optical media. The source files are bit-for-bit unchanged no
  matter what the guest does.
- **Is network-isolated by default** (`-nic none`); forwarding is opt-in
  via `MICROVM_FORWARDS`, same syntax as `run-microvm.sh`.
- **Runs under the same QEMU `-sandbox` seccomp/anti-privilege-escalation
  preset** as the existing fallback path.
- **Does not yet** run under gVisor/a second isolation layer, drop
  capabilities beyond QEMU's own `-sandbox`, or get its own conformance
  suite — see "Not yet done" below.

## Implemented (2026-08-05)

- **Format detection**: `internal/vmdisk.Detect` identifies RAW, QCOW2,
  VMDK (both binary sparse-extent and text-descriptor variants), VHD
  (header *and* footer-only/fixed-disk forms), VHDX, and ISO9660 by
  header inspection only — it never mounts or loop-attaches anything.
  Bounded to a 64 KiB leading read (+512 B trailing read for VHD
  footers) regardless of file size; fails closed
  (`vmdisk.ErrUnsupportedFormat`) on anything it can't positively
  identify — an unrecognized file is never guessed at or assumed to be
  RAW.
- **Logical block mapping**: for RAW, VHD (fixed and dynamic), VHDX
  (non-differencing), QCOW2 (uncompressed clusters), and VMDK (binary
  sparse extent), `internal/vmdisk` can resolve a *logical* disk offset
  to the right bytes inside the container file — walking each format's
  own internal indirection (VHD's Block Allocation Table, QCOW2's L1/L2
  tables, VMDK's Grain Directory/Grain Table, VHDX's BAT + Metadata
  region), not just reading file offset 0. Two things are deliberately
  **not** handled and return `vmdisk.ErrCannotMapLogicalDisk` instead of
  guessing: a compressed QCOW2 cluster, and the VMDK text-descriptor form
  (which points at separate extent files — resolving that safely means
  validating cross-file path references, a separate piece of work).
  Differencing VHDX (with a parent disk) is also refused. VHDX
  region/metadata checksums are not verified — only structurally parsed
  and bounds-checked.
- **Boot-partition scan**: `vmdisk.ScanBootPartition` reads a disk's
  partition table (through its logical block mapping) and reports
  positive bootability evidence: an MBR active/bootable partition flag,
  a GPT EFI System Partition, or a GPT "Legacy BIOS Bootable" attribute.
  A disk with a valid but non-bootable table (or no table at all) is a
  determinate "not bootable" result, not an error — this never reads
  filesystem content, only the partition table itself, within the same
  bounded window format detection uses.
- **Multi-disk boot selection**: `vmdisk.SelectBootDisk(paths,
  bootDiskOverride)` scans every given disk and picks the boot disk: the
  explicit override if given, the sole disk if only one is given,
  otherwise the one disk (and only one) with confirmed boot evidence. Any
  other outcome — zero confirmed, more than one confirmed — fails closed
  and asks for `--boot-disk`, rather than guessing. A disk that can't be
  scanned (see above) doesn't block a *different* disk's confirmed
  result; it just contributes no evidence either way.
- **BIOS/OVMF boot**: `scripts/microvm/run-legacy-disk.sh` takes one or
  more `DISK_IMAGE FORMAT` pairs (first pair = boot disk), attaches each
  via `-machine q35` + its own disposable overlay (or read-only `-cdrom`
  for ISO), and boots with QEMU's own firmware — no
  `-kernel`/`-initrd`/`-append`. Command-line order is preserved so the
  boot disk stays first regardless of how many secondary disks follow.
  Optional `MICROVM_LEGACY_BOOT_TIMEOUT` runs a bounded "did QEMU stay
  up" smoke check instead of attaching interactively; this is **not** a
  guest readiness probe.
- **`platform-factory microvm run-legacy-disk` CLI**: `--disk` is
  repeatable (one project, multiple disks); `--boot-disk` overrides
  auto-selection. Calls `vmdisk.SelectBootDisk` first and refuses to
  invoke QEMU at all when the boot disk can't be determined, printing
  every disk's format/evidence to stderr either way.
- **`platform-factory init` integration**: `pf init [DIR]` now also
  scans `DIR`'s own top-level files (not recursive) for anything
  `internal/vmdisk.Detect` recognizes as a disk. If any are found, the
  boot disk is resolved the same way (`--boot-disk` override → sole disk
  → single confirmed match) and, failing that, **`pf init` prompts
  interactively** on stdin/stdout asking which one is the boot disk. The
  result is recorded in the generated `platform-factory.yaml` under a
  new `legacy_disks: {boot, data}` field (paths relative to the project
  root) — purely descriptive today; nothing downstream consumes it yet,
  `microvm run-legacy-disk` still takes its own `--disk` flags directly.
  A directory with no ambiguity resolvable and no `--boot-disk`/stdin
  available fails the whole `init` closed (exit 2, nothing written)
  rather than picking one arbitrarily.
- **`pf init` interactive UX**: beyond the boot-disk prompt, `pf init`
  now also asks for a language/artifact when ecosystem detection is
  ambiguous or unknown, and prints the full plan with a final "proceed?
  [y/N]" confirmation before writing anything (declining aborts with
  nothing written, same as a build error would). All three prompts share
  **one** `bufio.Reader` over stdin for the whole run — using a fresh one
  per prompt would silently drop already-buffered-but-unread input
  between prompts. New `--language`/`--artifact` flags resolve the
  language non-interactively (skip the prompt outright, no second-guessing);
  `--yes` skips every remaining prompt.
- **No more `REPLACE_ME` placeholder junk (2026-08-05)**: `pf init` used
  to write `language: REPLACE_ME` / `artifact: REPLACE_ME/...` whenever
  detection failed non-interactively - a config that loaded (so `pf init`
  itself reported success) but was never actually buildable, failing
  later with confusing internal errors (`no built-in freeze adapter for
  "REPLACE_ME"`, `stat .../REPLACE_ME/...: no such file`) instead of a
  clear one up front. Now: a real language is a precondition for writing
  *any* language/artifact block at all - resolved by confident detection,
  an interactive answer, or `--language`/`--artifact`. If none of those
  apply and there's no legacy disk either, `pf init` refuses outright
  (exit 2, nothing written, message points at `--language` or an
  interactive re-run) instead of writing something that merely *looks*
  ready. **Pure legacy-disk projects are the one case that never needed
  a language at all** - `internal/project.Config.Validate` now exempts
  any config carrying `legacy_disks` from the language/artifact
  requirement entirely (the disk is the deliverable), so `pf init` in a
  disk-only directory writes a `legacy_disks`-only config with no
  language/artifact fields and no fake values.

## Not yet done

- **amd64 only.** The script hard-refuses on non-amd64 hosts today
  (`-machine q35` + SeaBIOS/OVMF wiring hasn't been done for arm64).
- **`legacy_disks` in the config is inert.** `pf build`/`pf publish`/etc.
  don't read it; only `pf init` writes it. Wiring `microvm
  run-legacy-disk` (or a future `pf run`) to consume it automatically is
  follow-on work.
- **Compressed QCOW2 clusters and VMDK's text-descriptor/multi-extent
  form** can't be logically mapped — see "Implemented" above. A disk in
  either shape fails boot-disk auto-detection and needs `--boot-disk`.
- **Differencing VHDX (with a parent disk)** is refused for the same
  reason.
- **No conversion to OCI.** This boot mode is entirely separate from
  `pf build`; there is no path from a legacy disk to an OCI image today.
- **No real hardware test yet.** `internal/vmdisk` is unit-tested against
  synthetic, hand-constructed headers proving internal round-trip
  consistency (my own writer and reader agree) — it has **not** been
  cross-validated against files actually produced by qemu-img, VMware,
  or Hyper-V, and `run-legacy-disk.sh` has not been run against a real
  disk image on real KVM hardware in this session (no QEMU in this
  sandbox). Do both before relying on this for anything real.
- **No additional sandboxing** beyond QEMU's built-in `-sandbox` preset,
  despite executing untrusted guest code — see "Trust model change"
  above.
- **UEFI vs. BIOS is not selectable.** QEMU's default (SeaBIOS) is used
  unconditionally; a disk requiring UEFI (`-bios OVMF.fd`) will not boot
  today.
- **`pf init`'s disk scan is a heuristic**, not a guarantee: any
  top-level file whose first bytes happen to match a disk format's magic
  gets treated as a disk candidate. Low risk in practice, not zero.

## Suggested next slice

In rough order: (1) real-hardware validation — both cross-checking
`internal/vmdisk` against real tool-produced files and an actual
`run-legacy-disk.sh` boot on real KVM hardware, (2) arm64 support, (3) an
explicit UEFI/OVMF flag, (4) wiring `legacy_disks` from `platform-factory.yaml`
into an actual run path instead of staying descriptive-only, (5) the VMDK
text-descriptor/extent-chain and QCOW2-compressed-cluster cases, with the
path-safety care cross-file extent resolution needs.
