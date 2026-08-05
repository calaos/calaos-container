# CC-033: Small scripts hardening (rollback, cache, fs, pull)

- **Phase:** P3
- **Priority:** P1
- **Size:** M
- **Status:** Done
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

- [x] All four: source `scripts/lib/calaos-common.sh` and adopt `set -euo pipefail` (with `|| true` where a non-zero is legitimately expected).
- [x] `calaos_rollback`: use `findmnt` (anchored, `--source`/`--target`) instead of `mount | grep` (`:5,47`); validate `snapNumber` is a non-empty integer before `snapper rollback` (`:55-67`); add a cleanup trap that unmounts `/mnt` and `/mnt/.snapshots`.
- [x] `load_containers_cache`: replace `return 1` with `exit 1` (`:41`); use the library's image parser/detection; robustly parse `Image=` (single match, ignore comments).
- [x] `init_calaosfs`: verify `/mnt/calaos` (`$fs`) is mounted before creating the OLA symlink / `chown -R` (`:42-45`); make the `/var/run` migration idempotent and safe (skip if already `/run`).
- [x] `pull_calaos_image`: exact service/image matching via the library; only remove images that are genuinely the old tag of the pulled image, not every non-matching tag (`:51,59`).

## Out of scope

- `config_calaos-boot` (the caller of `load_containers_cache`) — CC-032.
- `calaos_install` — CC-031.
- The shared library — CC-030.

## Breaking changes

None to interfaces. `pull_calaos_image` is explicitly retained (not removed) per product decision.

## Acceptance criteria

- [x] `shellcheck` clean and `bash -n` pass for all four scripts (needed for CC-035).
- [x] `calaos_rollback` with an unmatched `/proc/cmdline` fails clearly instead of calling `snapper rollback` with an empty argument.
- [x] `load_containers_cache` error path returns a proper non-zero exit (no "can only return" error).
- [x] `init_calaosfs` does not create a dangling OLA symlink when `/mnt/calaos` is unmounted.
- [x] `pull_calaos_image` removes only the intended stale image.

## Verification notes

All four scripts now start with `set -euo pipefail`, `. /usr/lib/calaos/calaos-common.sh`
and `trap 'die "Error on line $LINENO"' ERR`, and no longer carry their own
copy-pasted color/log blocks (now from the library).

**`bash -n` / `shellcheck`:** all four pass with zero findings, checked both
standalone and together with `scripts/lib/calaos-common.sh` in one invocation
(mirrors the CI `shellcheck` job, which discovers and lints all Calaos-owned
shell files in a single `shellcheck` call so `source=` directives resolve).
Two intentional, justified directives: `# shellcheck disable=SC2119` on the
argument-less `calaos_parse_cmdline` call in `calaos_rollback` (reads the real
`/proc/cmdline` by design), and pre-assigning `image_base`/`image_tag`/
`ibase`/`itag` to `""` in `pull_calaos_image` before `calaos_image_split`
writes them via `printf -v` (shellcheck can't trace that dynamic write-back).

**`calaos_rollback`:** `mount | grep /.snapshots` and `mount | grep
"rootfs.*overlay"` replaced by `findmnt --target /.snapshots`/`--target /`
(anchored on the mount table, not text). The snapshot number is extracted via
the library (`calaos_parse_cmdline` + `calaos_cmdline_get rootflags` +
`calaos_snapshot_number_from_subvol`) and validated against `^[0-9]+$` before
ever being used — an unmatched `/proc/cmdline` now `die`s with "Could not
determine snapshot number..." and `snapper rollback` is never invoked with an
empty (or any unvalidated) argument. A `register_cleanup` trap unmounts
`/mnt/.snapshots` and `/mnt` (guarded by `mountpoint -q`) on any exit path,
including a `snapper`/`arch-chroot` failure mid-rollback.

**`load_containers_cache`:** the `:41 return 1` (which errors with "can only
return from a function or sourced script" since the script runs as its own
process, not sourced) is now `exit 1`. `Image=` is parsed by reading the
`.container` file line by line, skipping `#`/`;`/blank lines and stopping at
the first `Image=` match — immune to inline comments and multiple `Image=`
lines. The broken `podman images | grep base | grep tag` presence check is
replaced by the library's `calaos_image_exists`.

**`init_calaosfs`:** the OLA symlink block (`rm -rf /etc/ola; ln -s
"$fs/ola" /etc/ola; chown -R olad:olad ...`) now only runs when `mountpoint -q
"$fs"` succeeds; otherwise it `warn`s and skips, leaving no symlink at all
(never a dangling one). The `/var/run` migration keeps its original
idempotency guard (already-correct symlink -> no-op) and replaces `mv
/var/run/* /run/` with `cp -a /var/run/. /run/` (merges/overwrites on
collision instead of `mv` erroring on directory/file clashes, and doesn't
error on an empty `/var/run` the way an unmatched `/var/run/*` glob passed
literally to `mv` did under `set -e`).

**`pull_calaos_image`:** `IMAGE_SRC` is split via the library's
`calaos_image_split` (correct on `registry:port/repo:tag` refs) instead of
`cut -d: -f1/2`. Service matching uses `systemctl list-units --type=service
--no-legend --plain "$SERVICE_NAME"` (systemctl's own unit-name filtering) in
place of `grep -q "$SERVICE_NAME"` (a substring match that could hit e.g.
`xcalaos-home.service-extra` when looking for `calaos-home.service`). Old-tag
removal now walks `podman images --format '{{.Repository}}:{{.Tag}}'` (exact
`repo:tag` references, like `calaos_image_exists`) instead of `podman images |
grep base | awk '{print $3}'` (word-split IDs, pipefail off), and only
`podman rmi --force`s a reference whose repo is exactly `image_base` **and**
whose tag differs from the tag just pulled — never a different repo, never the
just-pulled tag itself.

**Testing (static + function-level, mocked commands — no destructive command
ever run for real):** shellcheck/`bash -n` as above, plus a function-level
test harness run inside a `ghcr.io/calaos/debian_deb_build_base` container
with the real (unmodified) library installed at `/usr/lib/calaos/` and PATH
stubs for `podman`, `systemctl`, `findmnt`, `mountpoint`, `mount`, `umount`,
`arch-chroot`, `chown` (a stateful mount/umount/mountpoint trio backs a fake
mount table so the cleanup trap can be observed reacting to what was actually
"mounted"). 33/33 assertions passed:
- podman detection: reproduced the documented substring look-alike false
  positive (`podman images | grep fo | grep 1.2.3` incorrectly matches an
  image only tagged `foo:1.2.3`) with the OLD pattern, and confirmed the NEW
  `calaos_image_exists`-based check correctly reports it absent.
- `load_containers_cache`: correct `Image=` parsing (comment + duplicate
  lines ignored), cache removed only on verified success, clean `exit 1` (no
  "can only return" error) and cache preserved when verification fails, usage
  path.
- `pull_calaos_image`: nominal pull/stop/removal; removes only the stale
  same-repo old tag, never the just-pulled tag, never an unrelated repo;
  reproduced the old substring service-match false positive and confirmed the
  new exact match correctly reports "not running" for a look-alike unit
  (and does not stop anything in that case).
- `init_calaosfs`: no dangling OLA symlink (+ warning) when `/mnt/calaos` is
  unmounted, correct symlink + chown when mounted; `/var/run` idempotent
  no-op, empty-dir migration, and colliding-filename migration all complete
  without aborting, non-colliding files preserved.
- `calaos_rollback`: nominal rollback (correct validated snapshot number
  passed to `snapper`, both mounts unmounted on success); unmatched
  `/proc/cmdline` fails clearly and never reaches `snapper`/`arch-chroot`;
  missing `/.snapshots` device and non-overlay root both fail cleanly;
  `arch-chroot`/`snapper` failure mid-rollback aborts the script AND the
  cleanup trap still unmounts `/mnt/.snapshots` and `/mnt`.

No interface changes: all four scripts keep their original argument lists,
exit codes on success, and stdout/stderr message shapes relied on by callers
(`app/system.go:76` for `calaos_rollback`, `config_calaos-boot` for
`load_containers_cache`, systemd `ExecStartPre` for `init_calaosfs`,
`calaos_install`/update flows for `pull_calaos_image`).
