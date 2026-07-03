# CC-055: CLI client tests

- **Phase:** P5
- **Priority:** P2
- **Size:** M
- **Status:** Backlog
- **Depends on:** CC-023 (CLI robustness), CC-040 (final envelope the client parses)
- **Blocks:** —
- **Files owned (exclusive for this phase):**
  - `cmd/calaos-os/api/api_test.go` (new)

## Context

CC-023 gave the CLI client timeouts, early error checks, and differentiated exit codes; CC-040 unified the response envelope the client parses. This ticket tests the client SDK against a stub HTTP server so its parsing and timeout behavior are pinned.

## Work items

- [ ] Stand up an `httptest.Server` returning the CC-040 envelope for the relevant routes (update list/check/upgrade/status, network list/configure).
- [ ] Assert the client parses success and error envelopes correctly for every method (no per-endpoint special-casing regressions).
- [ ] Timeout: a server that sleeps beyond the configured client timeout → the client returns a timeout error within bound (not infinite hang).
- [ ] Error mapping: a 409 from the server surfaces as the appropriate client error / exit-code class.
- [ ] `toCIDR` unit tests: valid and invalid IP/netmask combinations.

## Out of scope

- Production code — test file only.
- Server-side behavior — CC-050..054.

## Acceptance criteria

- [ ] `cd cmd/calaos-os && go test -race ./...` passes.
- [ ] Timeout behavior asserted (bounded, not infinite).
- [ ] Envelope parsing covered for all client methods.

## Verification notes

_(filled by the implementing agent)_
