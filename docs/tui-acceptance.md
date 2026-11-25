# TUI acceptance recipe

This recipe verifies the interactive terminal surfaces of `platform-factory`
(`pf init` / `runtimetui`, `pf build` / `buildtui`, and
`pf marketplace tui` / `marketplacetui`) across keyboard-only usage, small
terminals, `NO_COLOR=1` / `TERM=dumb`, cancellation without disk mutation, and
retry after invalid input.

## Automated suite

Run the deterministic TUI test suite with both default and no-color terminal
environments:

```sh
cd cmd/tui
go test -count=1 ./...
NO_COLOR=1 TERM=dumb go test -count=1 ./...
```

## Manual end-to-end walkthrough

1. **Install and initialize (`pf init`)**
   - In a clean project directory (for example Python, Node.js, or .NET), run
     `pf init .`.
   - Verify the proposed plan prints every file to be written before asking
     for confirmation.
   - Press `n` (or Ctrl+C) to cancel: verify no `pf.yaml` or `pf.lock` is
     created on disk.
   - Re-run `pf init .`, approve the plan, and verify `runtimetui` opens on an
     interactive TTY when no runtime is configured yet.
   - Test **invalid input & retry**: choose *Pull a base image*, enter an
     unpinned tag (`python:3.12-slim`), press `Enter`, confirm the inline error
     (`image reference must be digest-pinned`), then enter a valid
     `@sha256:<64-hex>` reference or press `Esc` to go back to the choice list.
   - Verify skipping leaves `pf.yaml` without `runtime:` and recommends the
     exact CLI command `pf plugin provision-runtime`.

2. **Build reference confirmation (`pf build`)**
   - Run `pf build` on an interactive TTY (without `--yes`).
   - Navigate between `Image` and `Tag` using `Tab`, `Shift+Tab`, `Up`, and
     `Down`.
   - Enter whitespace in `Image` or `Tag` and press `Enter`: verify the prompt
     stays open with an inline error, clears the error on the next edit, and
     accepts the corrected value on retry.
   - Verify an empty `Tag` previews `<image>:latest` and embeds that exact
     reference on confirmation.
   - Press `Esc` or `Ctrl+C`: verify the build aborts cleanly without writing
     `.platform-factory/image`.

3. **Marketplace browser (`pf marketplace tui`)**
   - Resize the terminal to **40x12** (or run under `NO_COLOR=1 TERM=dumb`).
   - Verify no rendered line overflows 40 columns in either list view or detail
     view.
   - Verify typing search characters (`q`, `j`, `k`, `?`) never triggers quit
     or list shortcuts; `Tab` cycles sort, `Ctrl+V` toggles verified-only,
     `Ctrl+L` clears search, `Enter` opens detail, and `Esc` returns to list /
     exits.
   - Verify blocked unsigned or unkeyed installs display the exact flags
     accepted by `platform-factory marketplace tui` (`--key PUBLIC.pem` /
     `--allow-unsigned`).
   - Press `x` on an installed plugin, then press `n` (or any key other than
     `y`): verify removal is canceled without deleting files.

## Action-to-command alignment

| TUI surface | Announced action / guidance | Executed / CLI command |
| --- | --- | --- |
| `runtimetui` | Skip for now (`pf plugin provision-runtime` later) | `platform-factory plugin provision-runtime --language <lang> --image <ref@sha256:...> --dir <dir> --arch <amd64\|arm64>` |
| `buildtui` | `-> <image>:<tag>` embedded as `org.opencontainers.image.ref.name` | `oci.Build` embeds exact confirmed `<image>:<tag>` |
| `marketplacetui` | Empty index: `platform-factory marketplace sync` | `platform-factory marketplace sync` |
| `marketplacetui` | Trust gate: reopen with `--key PUBLIC.pem` / `--allow-unsigned` | `platform-factory marketplace tui --key PUBLIC.pem` / `--allow-unsigned` |
