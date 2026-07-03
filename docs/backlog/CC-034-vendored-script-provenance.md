# CC-034: Vendored script provenance

- **Phase:** P3
- **Priority:** P3
- **Size:** S
- **Status:** Backlog
- **Depends on:** CC-001 (the `.shellcheckrc` exclusion list must already exist)
- **Blocks:** CC-035
- **Files owned (exclusive for this phase):**
  - `scripts/genfstab` (header comment only)
  - `scripts/arch-chroot` (header comment only)
  - `grub-btrfs/` (README/notes only — do not alter logic)
  - `docs/VENDORED.md` (new)

## Context

Several scripts are copied third-party code with **no in-tree provenance**, so security-patch drift is invisible and licensing is unclear:

- `scripts/genfstab` (480 ln) — from Arch `arch-install-scripts` / util-linux (`genfstab:5,283,288`). Used at `calaos_install:253`.
- `scripts/arch-chroot` (253 ln) — from Arch `arch-install-scripts` (`arch-chroot:137-139`). Used throughout `calaos_install` and `calaos_rollback`.
- `grub-btrfs/grub-btrfsd` (340 ln), `grub-btrfs/41_snapshots-btrfs` — Antynea/grub-btrfs, **GPLv3** (`grub-btrfsd:2-3`, `41_snapshots-btrfs:3`).
- `grub-btrfs/config` — `GRUB_BTRFS_VERSION=4.12-master-2023-04-28` (`config:4`), locally edited (`GRUB_BTRFS_SUBMENUNAME="Calaos-OS snapshots"`).
- `initramfs/hooks/grub-btrfs-overlay`, `initramfs/scripts/local-bottom/grub-btrfs-overlay` — grub-btrfs overlayfs support.

## Work items

- [ ] Add a provenance header comment to `scripts/genfstab` and `scripts/arch-chroot`: upstream project, URL, the version/commit vendored, license, and a note of any local modifications. **Do not change their logic.**
- [ ] Add a note (README or header) to `grub-btrfs/` recording upstream (Antynea/grub-btrfs), version (`4.12-master-2023-04-28`), GPLv3 license, and the local edits made to `config`.
- [ ] Create `docs/VENDORED.md` — a single table listing every vendored file, its upstream source, pinned version/commit, license, local-modification summary, and how to re-sync.
- [ ] Confirm the `.shellcheckrc` exclusion list (created by CC-001) covers exactly these vendored paths (`scripts/genfstab`, `scripts/arch-chroot`, `grub-btrfs/**`, `initramfs/**`). If a path is missing, note it in this ticket's verification notes for CC-035/CC-001 — **do not edit CI files here.**

## Out of scope

- Editing CI/`.shellcheckrc` — CC-001 owns it; CC-035 flips the gate.
- Modifying vendored logic — never; only headers/docs.
- Calaos-owned scripts — CC-030/031/032/033.

## Breaking changes

None (documentation only).

## Acceptance criteria

- [ ] `docs/VENDORED.md` lists all vendored files with upstream/version/license.
- [ ] `scripts/genfstab` and `scripts/arch-chroot` carry provenance headers; their executable logic is byte-identical below the header.
- [ ] grub-btrfs GPLv3 attribution is recorded.

## Verification notes

_(filled by the implementing agent)_
