<div align="center">
  <h1>Molee</h1>
  <p><em>🐹 The Mac cleaner CLI you know, plus a local web dashboard.</em></p>
</div>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-GPL_v3-blue.svg?style=flat-square" alt="License"></a>
  <a href="https://github.com/tw93/Mole"><img src="https://img.shields.io/badge/fork%20of-tw93%2FMole-5b9dff.svg?style=flat-square" alt="Upstream"></a>
</p>

**Molee is a community fork of [tw93/Mole](https://github.com/tw93/Mole)** — the 68k-star open-source Mac cleanup CLI. The fork keeps the battle-tested `clean` / `uninstall` / `optimize` / `analyze` / `status` CLI fully intact and updatable from upstream, and adds what the upstream project deliberately leaves out:

- **A local, read-only web dashboard** (`mo gui` / `mee gui`): live system health, disk treemap, cleanup preview, app inventory, and cleanup history — in your browser, bound to `127.0.0.1` with a per-launch token.
- **Nothing else is deleted from the web UI.** The dashboard only previews and monitors. Deleting still happens exclusively through the audited CLI paths (5-layer deletion safety, whitelist protection, Trash-by-default).

## Why a fork

Upstream Mole is a terminal tool with a rock-solid architecture (bash command layer + Go collectors, JSON surfaces on every read path). Upstream explicitly keeps GUI/daemon features out of the CLI repo and ships its own native app. Molee takes the opposite bet: a zero-install **web** dashboard that wraps the CLI's existing machine-readable surfaces (`status --watch` NDJSON, `analyze --json`, `clean --dry-run` ledger, `uninstall --list`, `history --json`) — additive by design, so upstream improvements keep merging in.

## Build & run

Requires macOS 12+ and Go 1.26+.

```bash
git clone https://github.com/Roloria/Molee.git
cd Molee
make build          # builds bin/analyze-go, bin/status-go, bin/gui-go
./mee               # the CLI (same subcommands as upstream `mo`)
./mee gui           # starts the dashboard and opens your browser
```

`mee` is a thin alias over the upstream-compatible `mole` entrypoint, so everything you know from Mole keeps working: `mee clean --dry-run`, `mee uninstall slack`, `mee analyze`, `mee status --watch`, …

### Dashboard

| View | Source | Notes |
|---|---|---|
| **Dashboard** | `mole status --watch` (NDJSON → SSE) | Health score, CPU/memory/network live charts, top processes, thermal/battery/proxy chips |
| **Disk** | `mole analyze --json [path]` | Treemap with drill-down; `cleanable` candidates and large files highlighted |
| **Clean** | `mole clean --dry-run --json` + `clean --only-from` | Preview, select, and execute — every deletion flows through the CLI's safety engine (whitelist, protection, occupancy checks) and lands in the operations log |
| **Uninstall** | `mole uninstall --list` | App inventory with sizes and Homebrew detection — **read-only** |
| **History** | `mole history --json` | Freed-space-per-session chart, session and deletion logs |

Execution safety: the server only accepts paths inside your home directory (no top-level entries, never Molee's own state), and the CLI layer re-validates every deletion at its sink — protected, whitelisted, and in-use paths are always skipped. The clean whitelist is editable in the dashboard (`~/.config/mole/whitelist`, plain-text patterns).

New CLI surfaces added by this fork (kept minimal and upstream-mergeable):

```bash
mo clean --dry-run --json      # machine-readable preview: single-line JSON as the last stdout line
mo clean --json                # after a real run: {"mode":"clean_result","freed_kb":…,…}
mo clean --only-from FILE      # clean only the newline-separated paths in FILE (user-level, non-interactive)
```

Security model: loopback-only listener, random per-launch token (cookie + Bearer), Host/Origin validation, and an execution path restricted to nested user-owned paths with the CLI's full deletion-safety engine underneath. Stop the server with `Ctrl+C`.

Flags: `--addr 127.0.0.1:PORT`, `--interval 2s`, `--token …`, `--no-open` (pass `--open=false`), `--dev-static DIR` for frontend development.

## Keeping current with upstream

```bash
git remote add upstream https://github.com/tw93/Mole.git   # once
git fetch upstream && git merge upstream/main               # regularly
```

The fork's policy is to diverge as little as possible from upstream: CLI-layer changes stay upstream-mergeable (`--json` outputs, additive flags), and everything GUI lives in `cmd/gui` + `bin/gui.sh`. Conflicts are expected only in `README.md`.

## Roadmap

- [x] **P0** — fork infrastructure, rebrand, sync setup
- [x] **P1** — read-only web dashboard
- [x] **P2** — `clean --json` / `clean --only-from` CLI surfaces, GUI execution with live progress, whitelist editor
- [ ] **P2 remainder** — `--json` for purge/optimize/installer, optimize-whitelist editor
- [ ] **P3** — native app packaging (Wails), scheduled cleanup, duplicate finder, Homebrew management

## Credits & license

Molee is built on the excellent work of the [Mole](https://github.com/tw93/Mole) project and its contributors — all credit for the cleaning engine, safety model, and CLI design belongs there. Please use/star/report issues there for CLI behavior that is unchanged upstream.

- Code: **GPL-3.0** (same as upstream); see [LICENSE](LICENSE).
- Trademark: Molee is not affiliated with or endorsed by Mole or tw93; "Mole for Mac" (mole.fit) is a separate product.
