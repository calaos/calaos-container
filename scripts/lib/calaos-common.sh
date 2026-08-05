#!/bin/bash
# calaos-common.sh - Shared shell helpers for Calaos-OS system scripts.
#
# This file is a LIBRARY: it must be sourced, not executed.
#   . /usr/lib/calaos/calaos-common.sh
#   source /usr/lib/calaos/calaos-common.sh
#
# It consolidates logic that used to be copy-pasted across
# scripts/calaos_install, scripts/calaos_rollback and
# scripts/config_calaos-boot: ANSI color/log helpers, a strict-mode
# preamble, a die()/cleanup-trap helper, an eval-free /proc/cmdline
# parser and a correct podman image-presence check.
#
# Recommended preamble for a consumer script (strict-mode snippet):
#   set -euo pipefail
#   . /usr/lib/calaos/calaos-common.sh
#   trap 'die "Error on line $LINENO"' ERR
# (calaos_strict_mode below does the "set -euo pipefail" part for you if
# you prefer calling a function over pasting the line.)

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    echo "calaos-common.sh is a library; source it, don't execute it." >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# Colors
# ---------------------------------------------------------------------------

# Public color palette. Most of these are only used by scripts that source
# this library (via color()/info()/warn()/err()/green()), not within this
# file itself, so shellcheck can't see those use sites - hence the
# per-line disables below for the ones not referenced in this file.
NOCOLOR='\033[0m'
CYAN='\033[0;36m'
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
# shellcheck disable=SC2034
ORANGE='\033[0;33m'
# shellcheck disable=SC2034
BLUE='\033[0;34m'
# shellcheck disable=SC2034
PURPLE='\033[0;35m'
# shellcheck disable=SC2034
LIGHTGRAY='\033[0;37m'
# shellcheck disable=SC2034
DARKGRAY='\033[1;30m'
# shellcheck disable=SC2034
LIGHTRED='\033[1;31m'
# shellcheck disable=SC2034
LIGHTGREEN='\033[1;32m'
# shellcheck disable=SC2034
LIGHTBLUE='\033[1;34m'
# shellcheck disable=SC2034
LIGHTPURPLE='\033[1;35m'
# shellcheck disable=SC2034
LIGHTCYAN='\033[1;36m'
# shellcheck disable=SC2034
WHITE='\033[1;37m'

# color <color_code> <message...>
# Prints <message...> in the given ANSI <color_code> (one of the variables
# above), reset afterwards. Writes to stdout. Generic building block for
# scripts (e.g. banners) that need a color other than the semantic
# info/warn/err/green ones below.
color() {
    local code="$1"
    shift
    echo -e "${code}$*${NOCOLOR}"
}

# info <message...>
# Prints an informational message in cyan to stdout.
info() {
    echo -e "${CYAN}$*${NOCOLOR}"
}

# warn <message...>
# Prints a warning message in yellow to stderr.
warn() {
    echo -e "${YELLOW}$*${NOCOLOR}" >&2
}

# err <message...>
# Prints an error message in red to stderr.
err() {
    echo -e "${RED}$*${NOCOLOR}" >&2
}

# green <message...>
# Prints a message in green to stdout (typically a success message).
green() {
    echo -e "${GREEN}$*${NOCOLOR}"
}

# ---------------------------------------------------------------------------
# Strict mode / error handling
# ---------------------------------------------------------------------------

# calaos_strict_mode
# Enables "set -euo pipefail" for the calling script. Equivalent to pasting
# the line yourself; provided so scripts can opt in with one call. Must be
# called from the top-level script (not a subshell) to take effect there.
calaos_strict_mode() {
    set -euo pipefail
}

# die <message> [exit_code]
# Prints <message> in red to stderr and exits with exit_code (default 1).
die() {
    local msg="$1"
    local code="${2:-1}"
    err "$msg"
    exit "$code"
}

_CALAOS_CLEANUP_FUNCS=()

# register_cleanup <function_name>
# Registers a previously-defined shell function (called with no arguments)
# to run when the current script exits - normal exit, `die`, a `set -e`
# failure, or a signal - in LIFO order (most recently registered runs
# first). Wires a single EXIT trap on first use; safe to call multiple
# times. No eval is used. Do not also set your own EXIT/0 trap in a script
# that uses register_cleanup - it would replace this one.
register_cleanup() {
    _CALAOS_CLEANUP_FUNCS+=("$1")
    if [ "${#_CALAOS_CLEANUP_FUNCS[@]}" -eq 1 ]; then
        trap _calaos_run_cleanup EXIT
    fi
}

# _calaos_run_cleanup
# Internal: EXIT trap target, runs registered cleanup functions. Not meant
# to be called directly - use register_cleanup instead.
_calaos_run_cleanup() {
    local i
    for (( i = ${#_CALAOS_CLEANUP_FUNCS[@]} - 1; i >= 0; i-- )); do
        "${_CALAOS_CLEANUP_FUNCS[$i]}" || true
    done
}

# ---------------------------------------------------------------------------
# /proc/cmdline parsing (eval-free)
# ---------------------------------------------------------------------------

# Whitelist of kernel command-line keys calaos_parse_cmdline will extract.
# Covers the keys the existing scripts actually look for:
#   LABEL           - set by the live ISO (e.g. "live-efi") - calaos_install
#   calaos_install   - bare boot flag requesting the installer - config_calaos-boot
#   rootflags       - carries "subvol=@/.snapshots/N/snapshot" on snapshot
#                     boot entries - see calaos_snapshot_number_from_subvol
# Extend before calling calaos_parse_cmdline if a script needs another key,
# e.g.: CALAOS_CMDLINE_KEYS+=(root)
CALAOS_CMDLINE_KEYS=(LABEL calaos_install rootflags)

# Populated by calaos_parse_cmdline; read with calaos_cmdline_get.
declare -A CALAOS_CMDLINE=()

# calaos_parse_cmdline [cmdline_string]
# Parses the kernel command line (default: contents of /proc/cmdline, or
# the given string - handy for tests) into the CALAOS_CMDLINE associative
# array. Only keys listed in CALAOS_CMDLINE_KEYS are kept. A token of the
# form key=value sets CALAOS_CMDLINE[key]=value; a bare token (no '=', e.g.
# the "calaos_install" boot flag) sets CALAOS_CMDLINE[key]=1. Resets
# CALAOS_CMDLINE on every call. No eval is used anywhere.
calaos_parse_cmdline() {
    local cmdline="${1:-}"
    local -a tokens=()
    local token key value wanted k

    if [ -z "$cmdline" ]; then
        cmdline="$(cat /proc/cmdline)"
    fi

    CALAOS_CMDLINE=()
    read -ra tokens <<< "$cmdline"

    for token in "${tokens[@]}"; do
        case "$token" in
            *=*)
                key="${token%%=*}"
                value="${token#*=}"
                ;;
            *)
                key="$token"
                value="1"
                ;;
        esac

        wanted=0
        for k in "${CALAOS_CMDLINE_KEYS[@]}"; do
            if [ "$k" = "$key" ]; then
                wanted=1
                break
            fi
        done

        if [ "$wanted" -eq 1 ]; then
            CALAOS_CMDLINE["$key"]="$value"
        fi
    done
}

# calaos_cmdline_get <key>
# Prints the value captured for <key> by the last calaos_parse_cmdline
# call and returns 0, or prints nothing and returns 1 if <key> was not
# present on the command line (or isn't whitelisted).
calaos_cmdline_get() {
    local key="$1"
    if [ -n "${CALAOS_CMDLINE[$key]+x}" ]; then
        printf '%s' "${CALAOS_CMDLINE[$key]}"
        return 0
    fi
    return 1
}

# calaos_snapshot_number_from_subvol <value>
# Extracts the snapshot number N from a btrfs subvol path fragment of the
# form "subvol=@/.snapshots/N/snapshot", as found in the "rootflags" kernel
# parameter on grub-btrfs snapshot boot entries (see
# calaos_cmdline_get rootflags). Prints N and returns 0 on match; prints
# nothing and returns 1 otherwise. No eval is used.
calaos_snapshot_number_from_subvol() {
    local value="$1"
    local re='@/\.snapshots/([0-9]+)/snapshot'
    if [[ "$value" =~ $re ]]; then
        printf '%s' "${BASH_REMATCH[1]}"
        return 0
    fi
    return 1
}

# ---------------------------------------------------------------------------
# Podman image helpers
# ---------------------------------------------------------------------------

# calaos_image_split <image_ref> <base_var> <tag_var>
# Splits an image reference "repo[:tag]" into repository and tag, storing
# the results in the caller's <base_var>/<tag_var> variables (by name, via
# printf -v - no eval). A missing tag defaults to "latest", matching
# podman/docker's own default. Correctly handles registries with a port
# (e.g. "myregistry:5000/foo/bar:1.0") by only treating the final colon as
# a tag separator when what follows it contains no '/'.
calaos_image_split() {
    local image="$1" base_var="$2" tag_var="$3"
    # Prefixed to avoid colliding with the caller's <base_var>/<tag_var>
    # names: printf -v resolves to the nearest local of that name, so if
    # this function had its own "local base"/"local tag" and the caller
    # asked to write into variables also named "base"/"tag" (a very
    # natural choice), printf -v would silently set *this function's own*
    # locals instead of the caller's - the write-back would be lost.
    local _cis_base _cis_tag _cis_suffix

    case "$image" in
        *:*)
            _cis_suffix="${image##*:}"
            case "$_cis_suffix" in
                */*)
                    # The colon belongs to a "registry:port" host, not a tag.
                    _cis_base="$image"
                    _cis_tag="latest"
                    ;;
                *)
                    _cis_base="${image%:*}"
                    _cis_tag="$_cis_suffix"
                    ;;
            esac
            ;;
        *)
            _cis_base="$image"
            _cis_tag="latest"
            ;;
    esac

    printf -v "$base_var" '%s' "$_cis_base"
    printf -v "$tag_var" '%s' "$_cis_tag"
}

# calaos_image_exists <image_ref>
# Returns 0 if <image_ref> ("repo[:tag]", tag defaults to "latest") is
# present in the local podman image store, 1 otherwise. Matches exactly
# against `podman images --format '{{.Repository}}:{{.Tag}}'` output,
# avoiding the substring-match bugs of `podman images | grep "$ref"` (a
# ':'-containing ref never matches, since repo and tag are separate
# columns; a bare grep can also match unrelated repos/tags as substrings).
calaos_image_exists() {
    local image="$1" base tag want line

    calaos_image_split "$image" base tag
    want="${base}:${tag}"

    while IFS= read -r line; do
        if [ "$line" = "$want" ]; then
            return 0
        fi
    done < <(podman images --format '{{.Repository}}:{{.Tag}}')

    return 1
}
