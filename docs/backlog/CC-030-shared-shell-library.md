# CC-030: Shared shell library + strict-mode baseline

- **Phase:** P3
- **Priority:** P1
- **Size:** M
- **Status:** Done
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

- [x] Create `scripts/lib/calaos-common.sh` providing:
  - Color/log helpers (`info`, `warn`, `err`, `green`) defined once.
  - A strict-mode preamble helper or documented snippet (`set -euo pipefail`) plus a `die "msg"` function and a `register_cleanup` / trap helper.
  - An **eval-free** `/proc/cmdline` parser: read `/proc/cmdline`, split on whitespace, extract `key=value` pairs into shell variables via a whitelist (no `eval`).
  - A correct podman image-presence check using `podman images --format '{{.Repository}}:{{.Tag}}'` and an exact string match, plus a base/tag split helper.
- [x] The library itself must be shellcheck-clean (it becomes a merge gate in CC-035).
- [x] Install the library to the same location the scripts expect (e.g. `/usr/lib/calaos/` or alongside the sbin scripts) via `Makefile` install rule and `debian/calaos-container.install`, so a sourced path resolves on deployed systems.

## Out of scope

- Editing the consumer scripts to source the library — that is each of CC-031/032/033 (they own their own script files). This ticket only creates the library and its installation.
- Other Makefile fixes (`$(pwd)`, `PREFIX`, version) — CC-041.
- Vendored scripts (`genfstab`, `arch-chroot`) — never modify their logic; CC-034 only adds provenance headers.

## Breaking changes

None (additive). Deployed scripts keep working until CC-031/032/033 switch them to source the lib.

## Acceptance criteria

- [x] `shellcheck scripts/lib/calaos-common.sh` is clean.
- [x] The podman helper correctly reports presence/absence for an image like `ghcr.io/calaos/foo:1.2.3` (unit-test with a fake `podman` on PATH, or a documented manual check).
- [x] The cmdline parser extracts a known key from a sample `/proc/cmdline` string without `eval`.
- [x] `make install DESTDIR=... PREFIX=/usr` places the lib where scripts can source it.

## Verification notes

**Sourcing path contract for CC-031/032/033:** the library is installed to
`$(PREFIX)/lib/calaos/calaos-common.sh`, i.e. **`/usr/lib/calaos/calaos-common.sh`**
on a deployed/packaged system (`PREFIX=/usr`). Consumer scripts (`calaos_install`,
`calaos_rollback`, `config_calaos-boot`, `pull_calaos_image`,
`load_containers_cache`) should source it with a fixed absolute path:
```sh
. /usr/lib/calaos/calaos-common.sh
```
This mirrors how the scripts themselves are already installed to a fixed
`$(PREFIX)/sbin/` path with no relocatability handling, so a hardcoded
source path is consistent with the existing deployment model. The file is
installed `0644` (non-executable, sourced-only) and refuses to run if
executed directly (`BASH_SOURCE[0]` == `$0` guard).

**Function inventory** (see doc-comments in `scripts/lib/calaos-common.sh`
for full contracts):
- Colors: `NOCOLOR`, `RED`, `GREEN`, `CYAN`, `YELLOW`, `ORANGE`, `BLUE`,
  `PURPLE`, `LIGHTGRAY`, `DARKGRAY`, `LIGHTRED`, `LIGHTGREEN`, `LIGHTBLUE`,
  `LIGHTPURPLE`, `LIGHTCYAN`, `WHITE` - defined exactly once (the original
  scripts each redefined `CYAN` twice; fixed here).
- `color <color_code> <message...>` - generic colored print to stdout
  (drop-in for `config_calaos-boot`'s banner helper).
- `info`, `warn`, `err`, `green` - semantic log helpers. `info`/`green` go
  to stdout; `warn`/`err` go to **stderr** (the original scripts wrote
  `err` to stdout - fixed here; no call site relied on capturing its
  output).
- `calaos_strict_mode` - calls `set -euo pipefail` in the caller's shell.
- `die "msg" [exit_code]` - prints via `err`, exits (default code 1).
- `register_cleanup <function_name>` - LIFO cleanup-function registry,
  wires a single EXIT trap on first use. Scripts adopting this must not
  also set their own `trap ... 0`/`EXIT` - it would replace this one.
- `calaos_parse_cmdline [cmdline_string]` - eval-free `/proc/cmdline`
  parser (default source: `/proc/cmdline`; a string arg is accepted for
  testing). Populates the `CALAOS_CMDLINE` associative array, restricted
  to the `CALAOS_CMDLINE_KEYS` whitelist (default: `LABEL`,
  `calaos_install`, `rootflags` - the exact keys the three duplicated
  parsers in `calaos_install`/`config_calaos-boot`/`calaos_rollback` look
  for). Bare flags (no `=`, e.g. the `calaos_install` boot flag) are
  stored as `KEY=1`. Extend the whitelist before calling if a script needs
  another key: `CALAOS_CMDLINE_KEYS+=(root)`.
- `calaos_cmdline_get <key>` - reads a value out of `CALAOS_CMDLINE`.
- `calaos_snapshot_number_from_subvol <value>` - extracts the snapshot
  number `N` from a `subvol=@/.snapshots/N/snapshot` value fragment (the
  form found in the `rootflags` kernel parameter on grub-btrfs snapshot
  boot entries) - replacement for `calaos_rollback`'s inline regex.
- `calaos_image_split <image_ref> <base_var> <tag_var>` - splits
  `repo[:tag]` into base/tag (default tag `latest`), correctly handling a
  `registry:port` host with no tag (the old `cut -d: -f1/2` in the
  scripts breaks on that case).
- `calaos_image_exists <image_ref>` - exact-match presence check against
  `podman images --format '{{.Repository}}:{{.Tag}}'`. Replaces the broken
  `podman images | grep "$IMAGE_SRC"` pattern used in
  `calaos_install`/`load_containers_cache`/`pull_calaos_image` (a
  `:`-containing ref never matches that grep, since repo/tag are separate
  output columns; a bare `grep` can also false-positive on substrings,
  e.g. `foo` matching `foobar`).

**Verification performed:**
- `shellcheck scripts/lib/calaos-common.sh` -> 0 findings (shellcheck
  0.9.0, run on host).
- Sourced the library in a fresh `bash -c 'set -euo pipefail; . ...'` and
  called every helper - no crash; `info`/`warn`/`err`/`green`/`color`
  produce correctly-colored output on the expected stream;
  `calaos_strict_mode` correctly turns on `nounset` (verified an
  undefined-variable reference aborts afterwards).
- `calaos_image_split`/`calaos_image_exists` unit-tested against a fake
  `podman` shim on `PATH` printing canned `repo:tag` lines: correctly
  reports presence for `ghcr.io/calaos/foo:1.2.3` and for a bare
  `ghcr.io/calaos/bar` (implicit `:latest`); correctly reports absence for
  a substring look-alike (`ghcr.io/calaos/fo:1.2.3`, regression check for
  the old grep bug) and for a wrong tag; `calaos_image_split` verified
  against a `registry:port` reference with and without an explicit tag.
  Caught and fixed a real bug during this testing: `calaos_image_split`'s
  internal locals originally reused the names `base`/`tag`, which shadow
  a caller's identically-named target variables and silently swallow the
  `printf -v` write-back - renamed to `_cis_*` internally.
- `calaos_parse_cmdline`/`calaos_cmdline_get` unit-tested against sample
  cmdline strings covering all three original scripts' real keys: `LABEL`
  (calaos_install), bare `calaos_install` flag (config_calaos-boot), and
  `rootflags=subvol=@/.snapshots/N/snapshot` + `calaos_snapshot_number_from_subvol`
  (calaos_rollback) - all extracted correctly; a non-whitelisted key
  (`BOOT_IMAGE`) was correctly ignored; a crafted cmdline string
  containing shell metacharacters/command substitution in a value was
  parsed with no code execution, confirming no injection path exists.
  `grep -nE '^[^#]*\beval\b' scripts/lib/calaos-common.sh` (excluding
  comment lines) returns nothing - no `eval` invocation anywhere in the
  file, only doc-comment mentions of the word.
- `die` verified to print to stderr and exit with the requested code.
  `register_cleanup` verified to run registered functions in LIFO order
  on exit.
- `make install DESTDIR=<tmp> PREFIX=/usr` (run inside the
  `ghcr.io/calaos/debian_deb_build_base:latest` container, with build
  deps installed, since the C++ apt lib is a prerequisite of `install` on
  this repo and needs `libapt-pkg-dev`/`meson`/etc. not present on a bare
  host): places the file at `<tmp>/usr/lib/calaos/calaos-common.sh`, mode
  `0644`, content byte-identical to the source - matches the path in
  `debian/calaos-container.install` (`usr/lib/calaos`).
- `git status` confirms only `Makefile`, `debian/calaos-container.install`
  (one line each) and the new `scripts/lib/` are touched; no consumer
  script was modified.
