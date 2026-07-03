# CC-033: Small scripts hardening (rollback, cache, fs, pull)

- **Phase:** P3
- **Priority:** P1
- **Size:** M
- **Status:** Backlog
- **Depends on:** CC-030
- **Blocks:** CC-035
- **Files owned (exclusive for this phase):**
  - `scripts/calaos_rollback`
  - `scripts/load_containers_cache`
  - `scripts/init_calaosfs`
  - `scripts/pull_calaos_image`

## Context

Four smaller scripts, each with defects:

**`calaos_rollback` (71 ln)** — invoked from the API (`app/system.go:75`):
- `:5` `rootdev=$(mount | grep /.snapshots | awk '{print $1}')` — unanchored regex (`.` matches any char) can match multiple mount lines → multi-word `$rootdev`.
- `:47` `mount | grep "rootfs.*overlay"` text parsing.
- `:55-60` regex-extract snapshot number; if `/proc/cmdline` doesn't match, `$snapNumber` is unset and `:67 snapper … rollback $snapNumber` runs with an empty argument (no `-u`).
- `:63-71` unquoted vars; no cleanup trap — a failure leaves `/mnt` and `/mnt/.snapshots` mounted.

**`load_containers_cache` (44 ln)** — called from `config_calaos-boot:61`:
- No `set -e`.
- `:8` parses `Image=` via `grep … | cut -d= -f2` — breaks on multiple `Image=` lines / inline comments.
- `:34` pipeline `podman images | grep | grep` (pipefail off).
- **`:41` `return 1`** outside a function/sourced context → "can only return from a function or sourced script"; the error path is effectively broken (should be `exit 1`).

**`init_calaosfs` (56 ln)** — `ExecStartPre` of the daemon service:
- `:42-45` OLA symlink: `rm -fr /etc/ola` then symlink to `${fs}/ola`; if `/mnt/calaos` is not mounted this creates a dangling symlink and `chown -R` on it.
- `:48-55` `/var/run` migration `mv /var/run/* /run/` then `rm -rf /var/run` — under `set -e` a colliding `mv` aborts; racy against early-boot services.

**`pull_calaos_image` (72 ln)** — KEEP (installed on deployed systems), harden:
- `:37` `systemctl list-units … | grep -q "$SERVICE_NAME"` substring match.
- `:51` `for i in $(podman images | grep "$image_base" | awk '{print $3}')` — word-split IDs, pipefail off.
- `:59` `podman rmi --force "$i"` in a per-tag loop — force-removes by ID for every non-matching tag.

## Work items

- [ ] All four: source `scripts/lib/calaos-common.sh` and adopt `set -euo pipefail` (with `|| true` where a non-zero is legitimately expected).
- [ ] `calaos_rollback`: use `findmnt` (anchored, `--source`/`--target`) instead of `mount | grep` (`:5,47`); validate `snapNumber` is a non-empty integer before `snapper rollback` (`:55-67`); add a cleanup trap that unmounts `/mnt` and `/mnt/.snapshots`.
- [ ] `load_containers_cache`: replace `return 1` with `exit 1` (`:41`); use the library's image parser/detection; robustly parse `Image=` (single match, ignore comments).
- [ ] `init_calaosfs`: verify `/mnt/calaos` (`$fs`) is mounted before creating the OLA symlink / `chown -R` (`:42-45`); make the `/var/run` migration idempotent and safe (skip if already `/run`).
- [ ] `pull_calaos_image`: exact service/image matching via the library; only remove images that are genuinely the old tag of the pulled image, not every non-matching tag (`:51,59`).

## Out of scope

- `config_calaos-boot` (the caller of `load_containers_cache`) — CC-032.
- `calaos_install` — CC-031.
- The shared library — CC-030.

## Breaking changes

None to interfaces. `pull_calaos_image` is explicitly retained (not removed) per product decision.

## Acceptance criteria

- [ ] `shellcheck` clean and `bash -n` pass for all four scripts (needed for CC-035).
- [ ] `calaos_rollback` with an unmatched `/proc/cmdline` fails clearly instead of calling `snapper rollback` with an empty argument.
- [ ] `load_containers_cache` error path returns a proper non-zero exit (no "can only return" error).
- [ ] `init_calaosfs` does not create a dangling OLA symlink when `/mnt/calaos` is unmounted.
- [ ] `pull_calaos_image` removes only the intended stale image.

## Verification notes

_(filled by the implementing agent)_
