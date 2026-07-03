# CC-030: Shared shell library + strict-mode baseline

- **Phase:** P3
- **Priority:** P1
- **Size:** M
- **Status:** Backlog
- **Depends on:** CC-001 (shellcheck harness exists)
- **Blocks:** CC-031, CC-032, CC-033
- **Files owned (exclusive for this phase):**
  - `scripts/lib/calaos-common.sh` (new)
  - `Makefile` (the install rule for the new lib ONLY)
  - `debian/calaos-container.install` (add the lib)

## Context

The shell scripts duplicate the same logic 3–4× and share the same weaknesses. Consolidating first lets CC-031/032/033 each just source the library and focus on their script-specific hardening without re-implementing (and re-reviewing) the shared pieces.

Duplicated today:
- ANSI color block + `info`/`green`/`err` helpers copy-pasted verbatim across `scripts/calaos_install:7-38`, `scripts/calaos_rollback:7-38`, `scripts/config_calaos-boot:3-24` (each even redefines `CYAN` twice).
- `/proc/cmdline` parsing: `calaos_install:40-51` (`parse_cmdline`, uses `eval` — unsafe), `config_calaos-boot:26-37`, `calaos_rollback:55-60` (inline regex).
- IMAGE_SRC base/tag split + `podman images | grep` detection: `calaos_install:273-297`, `load_containers_cache:30-42`, `config_calaos-boot:52-68`, `pull_calaos_image:29-66`. The grep-based detection is **broken**: `grep "$IMAGE_SRC"` where `IMAGE_SRC` contains `:` never matches `podman images` output (repo and tag are separate columns).

## Work items

- [ ] Create `scripts/lib/calaos-common.sh` providing:
  - Color/log helpers (`info`, `warn`, `err`, `green`) defined once.
  - A strict-mode preamble helper or documented snippet (`set -euo pipefail`) plus a `die "msg"` function and a `register_cleanup` / trap helper.
  - An **eval-free** `/proc/cmdline` parser: read `/proc/cmdline`, split on whitespace, extract `key=value` pairs into shell variables via a whitelist (no `eval`).
  - A correct podman image-presence check using `podman images --format '{{.Repository}}:{{.Tag}}'` and an exact string match, plus a base/tag split helper.
- [ ] The library itself must be shellcheck-clean (it becomes a merge gate in CC-035).
- [ ] Install the library to the same location the scripts expect (e.g. `/usr/lib/calaos/` or alongside the sbin scripts) via `Makefile` install rule and `debian/calaos-container.install`, so a sourced path resolves on deployed systems.

## Out of scope

- Editing the consumer scripts to source the library — that is each of CC-031/032/033 (they own their own script files). This ticket only creates the library and its installation.
- Other Makefile fixes (`$(pwd)`, `PREFIX`, version) — CC-041.
- Vendored scripts (`genfstab`, `arch-chroot`) — never modify their logic; CC-034 only adds provenance headers.

## Breaking changes

None (additive). Deployed scripts keep working until CC-031/032/033 switch them to source the lib.

## Acceptance criteria

- [ ] `shellcheck scripts/lib/calaos-common.sh` is clean.
- [ ] The podman helper correctly reports presence/absence for an image like `ghcr.io/calaos/foo:1.2.3` (unit-test with a fake `podman` on PATH, or a documented manual check).
- [ ] The cmdline parser extracts a known key from a sample `/proc/cmdline` string without `eval`.
- [ ] `make install DESTDIR=... PREFIX=/usr` places the lib where scripts can source it.

## Verification notes

_(filled by the implementing agent)_
