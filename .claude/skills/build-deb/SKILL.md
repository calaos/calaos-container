---
name: build-deb
description: Build the Debian package locally the same way CI does, to validate packaging before pushing to main.
disable-model-invocation: true
---

Build the .deb package like the CI pipeline (`.github/workflows/build-deb-push.yml`):

1. Run the CI build container, mounting the repo at `/work`:
   ```bash
   docker run --rm -v "$(pwd)":/work ghcr.io/calaos/debian_deb_build_base /build-deb.sh
   ```
   Without a version argument, `build-deb.sh` derives one from `git describe`. Pass `$ARGUMENTS` as the version if the user provided one.
2. If docker or the image is unavailable, fall back to a native build (requires apt build deps): `apt-get build-dep -y . && dpkg-buildpackage -us -uc -b`.
3. The resulting .deb lands in `pkg/` (docker path) or the parent directory (native path). Report the package filename and size.
4. Warning: `build-deb.sh` edits `debian/rules` and `debian/changelog` in place to inject the version — after a build, check `git status` and revert those files if they were modified in the working tree.
