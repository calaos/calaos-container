# Refactoring Backlog — calaos-container

This directory holds the refactoring backlog produced from the technical-debt audit of
`calaos-container`. It is split into **independent, sequenced tickets** so each can be handed
to an autonomous developer (human or sub-agent) without merge conflicts.

- **Board:** [`/BOARD.md`](../../BOARD.md) (repo root) — kanban status of every ticket.
- **Tickets:** `docs/backlog/CC-NNN-slug.md` — one file per ticket.

## How the backlog is sequenced

The work is grouped into **6 phases**. A phase is a **merge barrier**: all tickets in phase N
merge before phase N+1 starts. Because of that barrier, cross-phase file overlap is harmless —
only **within-phase** overlap would cause conflicts, and within each phase the tickets' "Files
owned" sets are **pairwise disjoint**, so same-phase tickets run fully in parallel.

| Phase | Theme | Starts after | Notes |
|-------|-------|--------------|-------|
| **P0** | Foundation: CI + dead-code sweep | — | Merge CC-002 before CC-001 so the pipeline is green. |
| **P1** | Critical runtime/security fixes (Go) | P0 merged | 5 parallel tickets. |
| **P2** | Robustness / correctness (Go) | P1 merged | 4 parallel tickets. |
| **P3** | Shell hardening | P0 merged | **Runs concurrently with P1/P2** (disjoint file universe). CC-030 first, CC-035 last. |
| **P4** | Architecture: API contract + packaging | P1+P2+P3 merged | CC-040 runs alone vs `app/` and `cmd/`. |
| **P5** | Tests | P4 merged | 6 parallel tickets (all new `_test.go` files). |

## Dependency graph

```
CC-002 ─(merge order)→ CC-001 ─→ P1 {CC-010..014} ─→ P2 {CC-020..023} ─→ CC-040 ─→ P5 {CC-050..055}
   │                      │
   │                      ├─→ CC-030 ─→ {CC-031, CC-032, CC-033} ─┐
   │                      └─→ CC-034 ───────────────────────────→ ├─→ CC-035
   │                                                              ┘
   ├─→ CC-010   (models/update.go)
   └─→ CC-023   (cmd/calaos-os/*)

CC-010 ─→ CC-020        (apt call site in models/update.go)
CC-013 ─→ CC-021        (app/system.go install path)
CC-014 ─→ CC-042        (graceful shutdown → Restart/TimeoutStop)
P3 done ─→ CC-041       (Makefile last touched by CC-030)

P5 test deps:
  CC-050 ← CC-010, CC-020, CC-021
  CC-051 ← CC-012, CC-040
  CC-052 ← CC-011, CC-040
  CC-053 ← CC-014
  CC-054 ← CC-021, CC-022
  CC-055 ← CC-023, CC-040
```

## The hotspot files (why some tickets are sequenced, not split)

A few files attract many findings. Rather than split them into conflicting same-phase tickets,
they are touched by **at most one ticket per phase**:

- `models/update.go` — CC-002 (delete stub) → CC-010 (combined loop+race+lifecycle rewrite) → CC-020 (apt call site only).
- `app/system.go` — CC-013 (device/confirm guard) → CC-021 (streaming call site) → CC-040 (envelope).
- `app/app.go` — CC-002 (route/quitHeartbeat removal) → CC-014 (Fiber config/lifecycle) → CC-040 (error handler).
- `app/update.go` — CC-002 → CC-010 → CC-040.
- `cmd/calaos-os/*` — CC-002 → CC-023 → CC-040.
- `Makefile` — CC-001 (`test` target) → CC-030 (lib install rule) → CC-041 (everything else).

## Rules for an implementing agent

1. **Edit only the files in your ticket's "Files owned" list.** If you find a needed change in a
   file you do not own, do **not** edit it — leave a note in your PR referencing the ticket that
   owns it (see that file's finding in the audit).
2. **Do not start** until every ticket in your `Depends on` list is ✅ Done on the board.
3. Read the ticket's **Out of scope** section — it names adjacent findings deliberately left to
   other tickets, to stop "helpful" edits that cause conflicts.
4. Satisfy every **Acceptance criterion** and fill the **Verification notes** section with the
   commands you ran and their results.
5. Update your ticket's row in `/BOARD.md` (Backlog → In Progress → Review → Done) **in the same
   PR**. `BOARD.md` is the only intentionally shared file; keep edits to your one row.
6. Respect declared **Breaking changes** — coordinate external clients (calaos-home web UI) via
   `docs/API.md` (owned by CC-040).

## Ticket file template

```markdown
# CC-NNN: <Title>

- **Phase:** P0..P5
- **Priority:** P0..P3
- **Size:** S | M | L
- **Status:** Backlog | In Progress | Review | Done
- **Depends on:** CC-XXX (must be Done before start)
- **Blocks:** CC-ZZZ
- **Files owned (exclusive for this phase):**
  - path/one.go

## Context
<audit findings with file:line references>

## Work items
- [ ] ...

## Out of scope
<adjacent findings owned by other tickets>

## Breaking changes
<API/CLI/packaging contract changes, old → new; "None" if none>

## Removed features (for future reimplementation)
<removal tickets only>

## Acceptance criteria
- [ ] <verifiable, command + expected result where possible>

## Verification notes
<filled by the implementing agent>
```

## Ticket index

| ID | Title | Phase | Prio | Size |
|----|-------|-------|------|------|
| CC-001 | PR CI pipeline | P0 | P0 | M |
| CC-002 | Dead code & repo hygiene sweep | P0 | P1 | M |
| CC-010 | Update subsystem overhaul | P1 | P0 | L |
| CC-011 | Auth middleware hardening | P1 | P0 | S |
| CC-012 | Network validation & template fix | P1 | P0 | M |
| CC-013 | Install endpoint safety | P1 | P0 | S |
| CC-014 | Server & daemon lifecycle hardening | P1 | P0 | M |
| CC-020 | apt cgo error propagation & bounds fix | P2 | P1 | L |
| CC-021 | Command executor rewrite (mockable) | P2 | P1 | M |
| CC-022 | System info correctness | P2 | P2 | S |
| CC-023 | CLI robustness (calaos-os) | P2 | P2 | S |
| CC-030 | Shared shell library + strict mode | P3 | P1 | M |
| CC-031 | calaos_install hardening | P3 | P0 | L |
| CC-032 | config_calaos-boot hardening | P3 | P1 | M |
| CC-033 | Small scripts hardening | P3 | P1 | M |
| CC-034 | Vendored script provenance | P3 | P3 | S |
| CC-035 | Flip shellcheck to blocking | P3 | P1 | S |
| CC-040 | Unified API contract + CLI lockstep | P4 | P1 | L |
| CC-041 | Build & release engineering | P4 | P2 | M |
| CC-042 | systemd unit hardening | P4 | P2 | S |
| CC-050 | Update subsystem tests | P5 | P1 | M |
| CC-051 | Network validation tests | P5 | P1 | M |
| CC-052 | Auth middleware tests | P5 | P1 | S |
| CC-053 | Config & lifecycle tests | P5 | P2 | S |
| CC-054 | Command exec & system info tests | P5 | P1 | M |
| CC-055 | CLI client tests | P5 | P2 | M |
