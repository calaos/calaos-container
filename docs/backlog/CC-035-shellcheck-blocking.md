# CC-035: Flip shellcheck to blocking

- **Phase:** P3 (runs last in the phase)
- **Priority:** P1
- **Size:** S
- **Status:** Done
- **Depends on:** CC-031, CC-032, CC-033, CC-034 (all Calaos-owned scripts must be shellcheck-clean first)
- **Blocks:** —
- **Files owned (exclusive for this phase):**
  - `.github/workflows/ci.yml` (the shellcheck step only)
  - `.shellcheckrc` (final exclusion list only)

## Context

CC-001 introduced shellcheck as a **non-blocking** (`continue-on-error: true`) CI step so that the pipeline could go green before the scripts were cleaned. Once CC-030–CC-034 have made every Calaos-owned script shellcheck-clean and confirmed the vendored-file exclusions, the gate should become mandatory to prevent regression.

## Work items

- [ ] Remove `continue-on-error: true` from the shellcheck step in `.github/workflows/ci.yml`; shellcheck failures now block merges.
- [ ] Finalize `.shellcheckrc` so exactly the vendored paths are excluded (`scripts/genfstab`, `scripts/arch-chroot`, `grub-btrfs/**`, `initramfs/**`) and every Calaos-owned script + `scripts/lib/calaos-common.sh` is checked.
- [ ] Confirm the pipeline is green with the gate enabled.

## Out of scope

- Any script content — those are CC-030..034.
- Other CI steps (build, vet, lint, tests) — CC-001.

## Breaking changes

None to runtime; PRs introducing shellcheck violations in Calaos-owned scripts now fail.

## Acceptance criteria

- [ ] A PR that introduces a shellcheck violation in a Calaos-owned script fails CI.
- [ ] Vendored scripts still produce no annotations.
- [ ] Main is green with the blocking gate.

## Verification notes

_(filled by the implementing agent)_
