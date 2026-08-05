# CC-032: config_calaos-boot hardening

- **Phase:** P3
- **Priority:** P1
- **Size:** M
- **Status:** Done
- **Depends on:** CC-030
- **Blocks:** CC-035
- **Files owned (exclusive for this phase):**
  - `scripts/config_calaos-boot`

## Context

`config_calaos-boot` (205 lines) is the boot-time TUI (tty2) that offers install or loads containers. It has **no** `set -e`/`-u`/pipefail at all, and one genuinely dangerous behavior:

- `:29` `set -- $(cat /proc/cmdline)` — unquoted word-split.
- `:121-122` `mktemp` fallback `tmp_file=/tmp/tmp1$$` — predictable PID-based path (a trap cleanup at `:123` exists, good).
- `:147` `if < "$tmp_file2" grep -q "$disk"` — marks a disk "mounted" by substring match of the device name in `mount` output → false positives (`/dev/sda` matches `/dev/sda1`).
- **`:188-189`** `calaos_install "$disk"` then `reboot` with **no exit-status check** (no `set -e`): if the installer fails, the machine reboots anyway into a half-installed / unbootable state.
- `:61` `load_containers_cache …` return value ignored.

## Work items

- [x] Source `scripts/lib/calaos-common.sh`; adopt `set -euo pipefail` (carefully, given the interactive dialog flow — ensure the TUI still handles user cancel without aborting the whole script unexpectedly; use local `|| true` where a non-zero from `dialog`/`whiptail` is expected).
- [x] **Check the installer exit status** (`:188-189`): only `reboot` if `calaos_install` succeeded; on failure, show an error and drop to a retry prompt / shell rather than rebooting a broken system.
- [x] Replace the substring disk-mounted check (`:147`) with an exact-source match via `findmnt --source "$disk"` or `lsblk -no MOUNTPOINT "$disk"`.
- [x] Use the library's eval-free cmdline parser instead of `set -- $(cat /proc/cmdline)` (`:29`).
- [x] Check the `load_containers_cache` return (`:61`) and surface failures.

## Out of scope

- `calaos_install` internals — CC-031.
- `load_containers_cache` internals — CC-033.
- The shared library — CC-030.

## Breaking changes

None to the flow, but a failed install no longer results in an automatic reboot — this is the intended safety fix.

## Acceptance criteria

- [x] `shellcheck scripts/config_calaos-boot` clean; `bash -n` passes.
- [x] Simulated installer failure (stub `calaos_install` returning non-zero) does **not** reboot; it shows an error/retry path.
- [x] Disk-mounted detection no longer false-positives on partition-name substrings.

## Verification notes

**Static checks** (run against the final `scripts/config_calaos-boot`, matching the CI invocation with no `-x` flag):
- `bash -n scripts/config_calaos-boot` → exit 0, no syntax errors.
- `shellcheck scripts/config_calaos-boot` → exit 0, **zero findings**. The absolute `. /usr/lib/calaos/calaos-common.sh` source needed a targeted `# shellcheck disable=SC1091` (a bare `# shellcheck source=` comment does not suppress SC1091 for a not-present-at-lint-time absolute path without `-x`, which the CI job does not pass); the dynamically globbed `source "$source_file"` in `load_containers` similarly carries `# shellcheck disable=SC1090`.

**Function-level dynamic checks**: no real install/wipe was ever executed. All checks ran the unmodified `scripts/config_calaos-boot` (bind-mounted read-only) inside an ephemeral `ghcr.io/calaos/debian_deb_build_base:latest` container (same image used by CI/devcontainer), with the real, unmodified `scripts/lib/calaos-common.sh` installed at `/usr/lib/calaos/calaos-common.sh`, and a `/testbin` directory prepended to `PATH` providing stub binaries for every destructive/system command: `reboot`, `calaos_install` (scriptable exit code per scenario, logs its args), `load_containers_cache` (scriptable per-container success/failure), `chvt`, `udevadm`, `systemd-cat`, `dialog` (pops scripted `exit_code[:stderr text]` results per invocation from a queue file), and `lsblk` (dispatches on `-d`/`-no MOUNTPOINT` args to return fixed fake disk JSON or per-disk mountpoint text). Ten scenarios were exercised, all passing:

- **A** — nominal non-install boot: both fake containers load, no reboot, no `calaos_install` call, switches back to console 1.
- **B** — `load_containers_cache` failure for one container is surfaced (`warn`+log) while the loop still completes the remaining container(s), matching the "check the return, don't abort" work item.
- **C** — install flow, disk selected, `calaos_install` succeeds → reboots exactly once.
- **D** (acceptance criterion) — install flow, `calaos_install` **fails**: script does **not** reboot, prints an error message, and shows a second (retry) `dialog --yesno`; declining it falls back to `load_containers` + `chvt 1`, exits 0. Directly proves the `:188-189` "reboot regardless of exit status" bug is fixed.
- **E** — failed install, user chooses retry, second attempt succeeds: `calaos_install` invoked exactly twice, `reboot` called exactly once (only after the successful retry, never after the first failure).
- **F** — user declines the initial "install?" prompt (dialog exit 1): `set -euo pipefail` does not abort the script on the expected non-zero from `dialog`; `calaos_install` is never called; boot continues normally.
- **G** — user cancels the disk-selection radiolist (no disk chosen): reports "no disk selected", does not call `calaos_install`, does not crash, continues boot.
- **H** (acceptance criterion) — disk-mounted detection: `/dev/sda` (mounted) correctly flagged; `/dev/sdaa` — a device name for which the old `mount`-output substring/`grep -q "$disk"` check would false-positive, since `/dev/sda` is a literal substring of `/dev/sdaa1`'s mount line — is correctly reported as **not** mounted; a mounted NVMe partition and a genuinely free disk are both classified correctly. Confirms the `lsblk -no MOUNTPOINT "$disk"` replacement has no substring false-positive.
- **I** — `calaos_parse_cmdline`/`calaos_cmdline_get` integration: a bare `calaos_install` kernel flag is correctly detected, and a crafted/hostile cmdline value cannot inject a command (eval-free parser).
- **J** — an empty `/usr/share/calaos/*.source` glob (no container source files present) does not abort the script under `set -e` (guarded via `[ -e "$source_file" ] || continue`).

One real bug was found and fixed via this testing (not present in the original unhardened script, introduced then caught during hardening): bare `clear` fails under `set -e`/the `ERR` trap when `$TERM` is unset (as in a non-interactive container); fixed by guarding it as `clear || true`, consistent with how the other cosmetic/environment-dependent calls (`chvt`, `udevadm settle`) were already guarded.

No files outside `scripts/config_calaos-boot` (plus this ticket doc) were modified. The Docker-based test harness itself is scratch tooling, not part of the repository.
