---
name: smoke-test
description: Build everything, start the calaos-container daemon locally, and hit the REST API to confirm it works. Use to verify changes since there are no automated tests.
---

Smoke-test the daemon after changes:

1. Build: run `make` from the repo root. Fail fast and report compiler errors if it doesn't build.
2. Write a minimal config to the scratchpad (TOML):
   ```toml
   [general]
   port = 8000
   address = "127.0.0.1"
   ```
   Use a different port if 8000 is busy.
3. Start the daemon in the background: `LD_LIBRARY_PATH=./bin ./bin/calaos-container -c <config-path>`. Capture its log output.
4. Wait for it to listen, then curl read-only endpoints and check for valid JSON / HTTP 200:
   - `curl -s http://127.0.0.1:8000/api/system/info`
   - `curl -s http://127.0.0.1:8000/api/update/available`
   - `curl -s http://127.0.0.1:8000/api/network/list`
   If the change touched a specific endpoint, curl that one too (only GET endpoints by default — POST endpoints like reboot/install/upgrade mutate system state; only call those if the user explicitly asks).
5. Kill the daemon and report: build result, which endpoints responded, and any errors from the daemon log.
