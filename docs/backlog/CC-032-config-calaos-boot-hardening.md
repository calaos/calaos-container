# CC-032: config_calaos-boot hardening

- **Phase:** P3
- **Priority:** P1
- **Size:** M
- **Status:** Backlog
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

- [ ] Source `scripts/lib/calaos-common.sh`; adopt `set -euo pipefail` (carefully, given the interactive dialog flow — ensure the TUI still handles user cancel without aborting the whole script unexpectedly; use local `|| true` where a non-zero from `dialog`/`whiptail` is expected).
- [ ] **Check the installer exit status** (`:188-189`): only `reboot` if `calaos_install` succeeded; on failure, show an error and drop to a retry prompt / shell rather than rebooting a broken system.
- [ ] Replace the substring disk-mounted check (`:147`) with an exact-source match via `findmnt --source "$disk"` or `lsblk -no MOUNTPOINT "$disk"`.
- [ ] Use the library's eval-free cmdline parser instead of `set -- $(cat /proc/cmdline)` (`:29`).
- [ ] Check the `load_containers_cache` return (`:61`) and surface failures.

## Out of scope

- `calaos_install` internals — CC-031.
- `load_containers_cache` internals — CC-033.
- The shared library — CC-030.

## Breaking changes

None to the flow, but a failed install no longer results in an automatic reboot — this is the intended safety fix.

## Acceptance criteria

- [ ] `shellcheck scripts/config_calaos-boot` clean; `bash -n` passes.
- [ ] Simulated installer failure (stub `calaos_install` returning non-zero) does **not** reboot; it shows an error/retry path.
- [ ] Disk-mounted detection no longer false-positives on partition-name substrings.

## Verification notes

_(filled by the implementing agent)_
