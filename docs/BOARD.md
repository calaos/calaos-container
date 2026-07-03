# Refactoring Board — calaos-container

Tracking board for the refactoring backlog. Full tickets live in
[`docs/backlog/`](docs/backlog/); process and dependency graph in
[`docs/backlog/README.md`](docs/backlog/README.md).

**Legend:** ⬜ Backlog · 🔵 In Progress · 🟣 Review · ✅ Done

**Rules**
- A ticket may enter 🔵 In Progress only when every ticket in its **Deps** is ✅.
- **Phase gate:** all tickets of phase N must be ✅ before phase N+1 starts —
  **exception:** P3 (shell) may run concurrently with P1/P2 (disjoint files).
- Within a phase, tickets touch disjoint files → run them in parallel freely.
- The implementing agent updates its own row here in the same PR.

---

## P0 — Foundation
_Gate to start: none. Merge CC-002 before CC-001 so CI is green._

| ID | Title | Prio | Size | Deps | Status |
|----|-------|------|------|------|--------|
| CC-002 | Dead code & repo hygiene sweep | P1 | M | — | ⬜ |
| CC-001 | PR CI pipeline (build, vet, lint, race, shellcheck) | P0 | M | CC-002 (merge order) | ⬜ |

## P1 — Critical fixes (Go)
_Gate to start: P0 merged. 5 parallel tickets._

| ID | Title | Prio | Size | Deps | Status |
|----|-------|------|------|------|--------|
| CC-010 | Update subsystem overhaul (loop, race, lifecycle) | P0 | L | CC-002 | ⬜ |
| CC-011 | Auth middleware hardening | P0 | S | P0 | ⬜ |
| CC-012 | Network interface validation & template fix | P0 | M | P0 | ⬜ |
| CC-013 | Install endpoint safety | P0 | S | P0 | ⬜ |
| CC-014 | Server & daemon lifecycle hardening | P0 | M | P0 | ⬜ |

## P2 — Robustness (Go)
_Gate to start: P1 merged. 4 parallel tickets._

| ID | Title | Prio | Size | Deps | Status |
|----|-------|------|------|------|--------|
| CC-020 | apt cgo error propagation & bounds fix | P1 | L | CC-010 | ⬜ |
| CC-021 | Command executor rewrite + mockable executor | P1 | M | CC-013 | ⬜ |
| CC-022 | System info correctness | P2 | S | P1 | ⬜ |
| CC-023 | CLI robustness (calaos-os) | P2 | S | CC-002 | ⬜ |

## P3 — Shell hardening
_Gate to start: P0 merged. Runs concurrently with P1/P2. CC-030 first, CC-035 last._

| ID | Title | Prio | Size | Deps | Status |
|----|-------|------|------|------|--------|
| CC-030 | Shared shell library + strict-mode baseline | P1 | M | CC-001 | ⬜ |
| CC-031 | calaos_install hardening (destructive path) | P0 | L | CC-030 | ⬜ |
| CC-032 | config_calaos-boot hardening | P1 | M | CC-030 | ⬜ |
| CC-033 | Small scripts hardening (rollback, cache, fs, pull) | P1 | M | CC-030 | ⬜ |
| CC-034 | Vendored script provenance | P3 | S | CC-001 | ⬜ |
| CC-035 | Flip shellcheck to blocking | P1 | S | CC-031, CC-032, CC-033, CC-034 | ⬜ |

## P4 — Architecture & packaging
_Gate to start: P1 + P2 + P3 merged._

| ID | Title | Prio | Size | Deps | Status |
|----|-------|------|------|------|--------|
| CC-040 | Unified API contract + CLI lockstep | P1 | L | all P1 + P2 | ⬜ |
| CC-041 | Build & release engineering | P2 | M | P3 complete | ⬜ |
| CC-042 | systemd unit hardening | P2 | S | CC-014 | ⬜ |

## P5 — Tests
_Gate to start: P4 merged. 6 parallel tickets (new files only)._

| ID | Title | Prio | Size | Deps | Status |
|----|-------|------|------|------|--------|
| CC-050 | Update subsystem tests | P1 | M | CC-010, CC-020, CC-021 | ⬜ |
| CC-051 | Network validation tests | P1 | M | CC-012, CC-040 | ⬜ |
| CC-052 | Auth middleware tests | P1 | S | CC-011, CC-040 | ⬜ |
| CC-053 | Config & lifecycle tests | P2 | S | CC-014 | ⬜ |
| CC-054 | Command exec & system info tests | P1 | M | CC-021, CC-022 | ⬜ |
| CC-055 | CLI client tests | P2 | M | CC-023, CC-040 | ⬜ |

---

## Done log

| ID | Merged PR | Date | Notes |
|----|-----------|------|-------|
| _(none yet)_ | | | |
