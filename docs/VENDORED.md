# Vendored Third-Party Code

This document lists all third-party code vendored in the calaos-container repository, including upstream source, license, version, and modification status.

## Scripts

### `scripts/genfstab`

- **Upstream Source:** [Arch Linux arch-install-scripts](https://github.com/archlinux/arch-install-scripts)
- **License:** GPLv2+
- **Version:** Unknown (exact version/commit not recorded at time of vendoring)
- **Modification Status:** Yes
  - M4 preprocessing macros (`fstab-helpers`) have been expanded inline.
  - The script is self-contained and does not require build-time preprocessing.
  - Functional logic is identical to upstream; only build artifacts differ.
- **Used By:** `calaos_install` (line 253)
- **Lines:** 480
- **File Encoding:** Text (bash)

### `scripts/arch-chroot`

- **Upstream Source:** [Arch Linux arch-install-scripts](https://github.com/archlinux/arch-install-scripts)
- **License:** GPLv2+
- **Version:** Unknown (exact version/commit not recorded at time of vendoring)
- **Modification Status:** Yes
  - M4 preprocessing macros (`common`) have been expanded inline.
  - The script is self-contained and does not require build-time preprocessing.
  - Functional logic is identical to upstream; only build artifacts differ.
- **Used By:** `calaos_install`, `calaos_rollback`
- **Lines:** 253
- **File Encoding:** Text (bash)

## GRUB-BTRFS (Btrfs Snapshot Support)

### `grub-btrfs/grub-btrfsd`

- **Upstream Source:** [Antynea/grub-btrfs](https://github.com/Antynea/grub-btrfs)
- **License:** GPLv3
- **Version:** Unknown (exact version at vendoring time not recorded)
- **Modification Status:** No (contains original copyright and license headers)
- **Lines:** 340
- **File Encoding:** Text (bash)
- **Copyright:** Copyright 2023 Pascal Jaeger

### `grub-btrfs/41_snapshots-btrfs`

- **Upstream Source:** [Antynea/grub-btrfs](https://github.com/Antynea/grub-btrfs)
- **License:** GPLv3
- **Version:** Unknown (exact version at vendoring time not recorded)
- **Modification Status:** No (contains original attribution headers)
- **Lines:** ~250+
- **File Encoding:** Text (bash)
- **Author:** Antynea (see GitHub above)
- **Note:** Automatically generates GRUB menu entries for Btrfs snapshots; supports manual, snapper, and timeshift snapshots.

### `grub-btrfs/config`

- **Upstream Source:** [Antynea/grub-btrfs](https://github.com/Antynea/grub-btrfs) config template
- **License:** GPLv3 (same as parent project)
- **Version:** 4.12-master-2023-04-28T16:26:00+00:00 (locally vendored)
- **Current Upstream Version:** master-2026-08-05T07:40:52+00:00
- **Modification Status:** Yes (Locally Customized)
  - `GRUB_BTRFS_SUBMENUNAME="Calaos-OS snapshots"` (custom menu name; upstream uses distribution detection)
  - Version is significantly older than upstream (April 2023 vs. current).
- **Lines:** 147
- **File Encoding:** Text (bash configuration)

## CI/Linting Exclusions

The following files are excluded from shellcheck linting in `.github/workflows/ci.yml` (they are third-party or custom extensions with build-specific or non-standard patterns):

```bash
git ls-files \
  | grep -vE '^(scripts/genfstab|scripts/arch-chroot|grub-btrfs/)' \
  | ...
```

Rationale:
- **scripts/genfstab, scripts/arch-chroot:** Vendored from upstream with M4 macros expanded; patterns may not conform to calaos-container's shellcheck configuration.
- **grub-btrfs/:** Third-party project files with their own linting standards.

## Summary Table

| File | Upstream | License | Version | Modified | Reason |
|------|----------|---------|---------|----------|--------|
| `scripts/genfstab` | archlinux/arch-install-scripts | GPLv2+ | Unknown | Yes | M4 macros expanded inline |
| `scripts/arch-chroot` | archlinux/arch-install-scripts | GPLv2+ | Unknown | Yes | M4 macros expanded inline |
| `grub-btrfs/grub-btrfsd` | Antynea/grub-btrfs | GPLv3 | Unknown | No | Original unmodified |
| `grub-btrfs/41_snapshots-btrfs` | Antynea/grub-btrfs | GPLv3 | Unknown | No | Original unmodified |
| `grub-btrfs/config` | Antynea/grub-btrfs | GPLv3 | 4.12-master-2023-04-28 | Yes | Custom menu name, version drift |

## Security & Maintenance Notes

1. **arch-install-scripts** (`genfstab`, `arch-chroot`):
   - No version tracking means security updates cannot be easily detected.
   - Consider tracking the upstream commit hash if vendoring is needed in the future.

2. **grub-btrfs**:
   - `config` file is outdated (April 2023 vs. current August 2026).
   - If updating, verify compatibility with current grub-btrfs version.
   - GPLv3 licensing must be respected; see `grub-btrfs/` files for license text.

## References

- [Arch Linux arch-install-scripts Repository](https://github.com/archlinux/arch-install-scripts)
- [Antynea/grub-btrfs Repository](https://github.com/Antynea/grub-btrfs)
- [GNU General Public License v2](https://www.gnu.org/licenses/gpl-2.0.html)
- [GNU General Public License v3](https://www.gnu.org/licenses/gpl-3.0.html)
