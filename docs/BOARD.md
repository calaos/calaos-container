# Refactoring Board — calaos-container

Tracking board for the refactoring backlog. Full tickets live in
[`docs/backlog/`](docs/backlog/); process and dependency graph in
[`docs/backlog/README.md`](docs/backlog/README.md).

**Legend:** ⬜ Backlog · 🔵 In Progress · 🟣 Review · ✅ Done · ➖ Absorbed

**Rules**
- A ticket may enter 🔵 In Progress only when every ticket in its **Deps** is ✅.
- **Phase gate:** all tickets of phase N must be ✅ before phase N+1 starts —
  **exception:** P3 (shell) may run concurrently with P1/P2 (disjoint files).
- **P6 exception:** P6 has its own gate (CC-040 + CC-042 ✅, ADM-CH1a done); it does not wait for CC-041 or P5. Absorbed tickets (➖) do not hold a phase open.
- Within a phase, tickets touch disjoint files → run them in parallel freely.
- The implementing agent updates its own row here in the same PR.

---

## P0 — Foundation
_Gate to start: none. Merge CC-002 before CC-001 so CI is green._

| ID | Title | Prio | Size | Deps | Status |
|----|-------|------|------|------|--------|
| CC-002 | Dead code & repo hygiene sweep | P1 | M | — | ✅ |
| CC-001 | PR CI pipeline (build, vet, lint, race, shellcheck) | P0 | M | CC-002 (merge order) | ✅ |

## P1 — Critical fixes (Go)
_Gate to start: P0 merged. 5 parallel tickets._

| ID | Title | Prio | Size | Deps | Status |
|----|-------|------|------|------|--------|
| CC-010 | Update subsystem overhaul (loop, race, lifecycle) | P0 | L | CC-002 | ✅ |
| CC-011 | Auth middleware hardening | P0 | S | P0 | ✅ |
| CC-012 | Network interface validation & template fix | P0 | M | P0 | ✅ |
| CC-013 | Install endpoint safety | P0 | S | P0 | ✅ |
| CC-014 | Server & daemon lifecycle hardening | P0 | M | P0 | ✅ |

## P2 — Robustness (Go)
_Gate to start: P1 merged. 4 parallel tickets._

| ID | Title | Prio | Size | Deps | Status |
|----|-------|------|------|------|--------|
| CC-020 | apt cgo error propagation & bounds fix | P1 | L | CC-010 | ✅ |
| CC-021 | Command executor rewrite + mockable executor | P1 | M | CC-013 | ✅ |
| CC-022 | System info correctness | P2 | S | P1 | ✅ |
| CC-023 | CLI robustness (calaos-os) | P2 | S | CC-002 | ✅ |

## P3 — Shell hardening
_Gate to start: P0 merged. Runs concurrently with P1/P2. CC-030 first, CC-035 last._

| ID | Title | Prio | Size | Deps | Status |
|----|-------|------|------|------|--------|
| CC-030 | Shared shell library + strict-mode baseline | P1 | M | CC-001 | ✅ |
| CC-031 | calaos_install hardening (destructive path) | P0 | L | CC-030 | ✅ |
| CC-032 | config_calaos-boot hardening | P1 | M | CC-030 | ✅ |
| CC-033 | Small scripts hardening (rollback, cache, fs, pull) | P1 | M | CC-030 | ✅ |
| CC-034 | Vendored script provenance | P3 | S | CC-001 | ✅ |
| CC-035 | Flip shellcheck to blocking | P1 | S | CC-031, CC-032, CC-033, CC-034 | ✅ |

## P4 — Architecture & packaging
_Gate to start: P1 + P2 + P3 merged._

| ID | Title | Prio | Size | Deps | Status |
|----|-------|------|------|------|--------|
| CC-040 | Unified API contract under a versioned prefix + CLI lockstep, admin section of `API.md` | P1 | L | all P1 + P2 | ✅ |
| CC-041 | Build & release engineering | P2 | M | P3 complete | ✅ |
| CC-042 | systemd unit hardening + postinst with deferred restart (absorbs CC4) | P2 | S | CC-014 | ⬜ |
| [CC-043](backlog/CC-043-initramfs-overlay-hook-path.md) | grub-btrfs-overlay initramfs hook/script installed as directories, never run | P2 | S | CC-041 | ⬜ |

## P5 — Tests
_Gate to start: P4 merged. 6 parallel tickets (new files only)._

| ID | Title | Prio | Size | Deps | Status |
|----|-------|------|------|------|--------|
| CC-050 | Update subsystem tests (absorbed by CC3) | P1 | M | CC-010, CC-020, CC-021 | ➖ |
| CC-051 | Network validation tests | P1 | M | CC-012, CC-040 | ⬜ |
| CC-052 | Auth middleware tests (absorbed by CC2) | P1 | S | CC-011, CC-040 | ➖ |
| CC-053 | Config & lifecycle tests | P2 | S | CC-014 | ⬜ |
| CC-054 | Command exec & system info tests | P1 | M | CC-021, CC-022 | ⬜ |
| CC-055 | CLI client tests (absorbed by CC3) | P2 | M | CC-023, CC-040 | ➖ |

## P6 — Admin
_Gate to start: CC-040 and CC-042 merged, ADM-CH1a done. Sequential: CC2, CC1, CC3. Plan: [`docs/admin/PLAN.md`](https://github.com/calaos/calaos_base/blob/docs/admin-webui-plan/docs/admin/PLAN.md) (calaos_base)._

| ID | Title | Prio | Size | Deps | Status |
|----|-------|------|------|------|--------|
| [ADM-CC2](backlog/ADM-CC2-admin-token.md) | 32-byte urandom token, `?token=` scoping; absorbs CC-052 | P1 | S | CC-040, CC-042, ADM-CH1a | ⬜ |
| [ADM-CC1](backlog/ADM-CC1-admin-password.md) | Admin password, sessions, `admin/*` routes, CLI `set/reset` | P1 | M | CC-040, ADM-CC2 | ⬜ |
| [ADM-CC3](backlog/ADM-CC3-async-upgrade.md) | Async versioned upgrade (202 + job), `reboot_required`; absorbs CC-050, CC-055 | P1 | L | ADM-CC1 | ⬜ |
| [ADM-CC2b](backlog/ADM-CC2b-retire-query-token.md) | Drop `?token=` on `/events` (conditional) | P3 | S | ADM-CH1a finding, CH1b + one release | ⬜ |

---

## Done log

| ID | Commit on main | Date | Notes |
|----|-----------|------|-------|
| CC-001 | `ac5969b` | 2026-08-05 | On main, see `git log` |
| CC-002 | `8a50c87` | 2026-08-05 | On main, see `git log` |
| CC-010 | `e57ea99` | 2026-08-05 | On main, see `git log` |
| CC-011 | `f7fd6b2` | 2026-08-05 | On main, see `git log` |
| CC-012 | `3501436` | 2026-08-05 | On main, see `git log` |
| CC-013 | `90f2658` | 2026-08-05 | On main, see `git log` |
| CC-014 | `8d314d9` | 2026-08-05 | On main, see `git log` |
| CC-020 | `3be5b7d` | 2026-08-05 | On main, see `git log` |
| CC-021 | `c96e7ef` | 2026-08-05 | On main, see `git log` |
| CC-022 | `1638f8b` | 2026-08-05 | On main, see `git log` |
| CC-023 | `0b8c35d` | 2026-08-05 | On main, see `git log` |
| CC-030 | `e608475` | 2026-08-05 | On main, see `git log` |
| CC-031 | `a2747e8` | 2026-08-05 | On main, see `git log` |
| CC-032 | `a0621e2` | 2026-08-05 | On main, see `git log` |
| CC-033 | `031bc97` | 2026-08-05 | On main, see `git log` |
| CC-034 | `6725eb6` | 2026-08-05 | On main, see `git log` |
| CC-035 | `e1e266d` | 2026-08-05 | On main, see `git log` |
| CC-040 | `67a3999` + implementation PR | 2026-10-22 | `/api/v2` contract and implementation; legacy routes pinned by golden tests |
