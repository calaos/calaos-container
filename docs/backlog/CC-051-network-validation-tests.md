# CC-051: Network validation tests

- **Phase:** P5
- **Priority:** P1
- **Size:** M
- **Status:** Backlog
- **Depends on:** CC-012 (validation + `text/template`), CC-040 (final envelope/status codes)
- **Blocks:** —
- **Files owned (exclusive for this phase):**
  - `models/network_test.go` (new)
  - `app/network_test.go` (new)

## Context

CC-012 closed the path-traversal and directive-injection holes and switched to `text/template`; CC-040 finalized the response shape. This ticket pins those with a malicious-input corpus and a golden-file comparison so the security fix cannot silently regress.

## Work items

- [ ] Interface-name validation corpus: `../`, `..%2F`, absolute paths, names >15 chars, empty, names with `/`, valid names → assert 400 for bad, pass for good; assert **no file is created** for rejected inputs (use a temp dir / injected path prefix if the code allows, otherwise assert at the validation-function level).
- [ ] Body-field injection corpus: newline/`\r` in `Gateway`/`DNSServers`/`SearchDomains`, non-IP values, valid values → assert rejection of malicious, acceptance of valid.
- [ ] Golden-file test: render a known-good static config and compare the generated `.network` content byte-for-byte to a checked-in golden file (proves `text/template`, no HTML entities, no injected directives).
- [ ] Handler-level test through Fiber (`app.Test`) asserting the CC-040 envelope and 400 codes.

## Out of scope

- Production code changes — test files only.
- Auth — CC-052.

## Acceptance criteria

- [ ] `go test ./models/ ./app/ -run Network` passes.
- [ ] Every traversal/injection vector is asserted to 400 with no file write.
- [ ] Golden file matches; changing the template escaping breaks the test.

## Verification notes

_(filled by the implementing agent)_
