package app

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

// resetTokenCache points the package-level token cache at path and clears
// all cached state, so each test starts from a clean slate independent of
// execution order. Previous state is restored on test cleanup.
func resetTokenCache(t *testing.T, path string) {
	t.Helper()
	prevFile, prevStr, prevValid := tokenFile, tokenStr, tokenValid
	prevPath, prevMod, prevSize := tokenLoadPath, tokenModTime, tokenSize

	tokenFile = path
	tokenStr = ""
	tokenValid = false
	tokenLoadPath = ""
	tokenModTime = time.Time{}
	tokenSize = 0

	t.Cleanup(func() {
		tokenFile = prevFile
		tokenStr = prevStr
		tokenValid = prevValid
		tokenLoadPath = prevPath
		tokenModTime = prevMod
		tokenSize = prevSize
	})
}

func TestCurrentToken_MissingFile(t *testing.T) {
	dir := t.TempDir()
	resetTokenCache(t, filepath.Join(dir, "does-not-exist"))

	if _, ok := currentToken(); ok {
		t.Fatal("expected ok=false for a missing token file")
	}
}

func TestCurrentToken_EmptyFileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	tf := filepath.Join(dir, "token")
	if err := os.WriteFile(tf, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	resetTokenCache(t, tf)

	got, ok := currentToken()
	if ok {
		t.Fatalf("expected ok=false for an empty/whitespace token file, got token %q", got)
	}
}

func TestCurrentToken_ValidFile(t *testing.T) {
	dir := t.TempDir()
	tf := filepath.Join(dir, "token")
	if err := os.WriteFile(tf, []byte("s3cr3t\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	resetTokenCache(t, tf)

	got, ok := currentToken()
	if !ok || got != "s3cr3t" {
		t.Fatalf("expected (%q, true), got (%q, %v)", "s3cr3t", got, ok)
	}
}

// TestCurrentToken_Rotation covers the no-restart-required rotation
// requirement: rewriting the token file must be picked up by the next
// request without any process restart or explicit cache reset.
func TestCurrentToken_Rotation(t *testing.T) {
	dir := t.TempDir()
	tf := filepath.Join(dir, "token")
	if err := os.WriteFile(tf, []byte("first"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	resetTokenCache(t, tf)

	if got, ok := currentToken(); !ok || got != "first" {
		t.Fatalf("initial load: got (%q, %v)", got, ok)
	}

	// Different length than "first" so the change is detected via size,
	// independent of filesystem mtime granularity.
	if err := os.WriteFile(tf, []byte("second-rotated"), 0o600); err != nil {
		t.Fatalf("rewrite token file: %v", err)
	}

	got, ok := currentToken()
	if !ok || got != "second-rotated" {
		t.Fatalf("expected rotated token to be picked up without restart, got (%q, %v)", got, ok)
	}
}

// TestNewTokenMiddleware_FailsClosedAndAcceptsValidToken is a minimal
// end-to-end smoke matrix for the middleware, covering the defects fixed by
// CC-011: an empty token file must reject even a request with no
// Authorization header, and a provisioned token must accept only the
// correct value. Comprehensive middleware coverage is CC-052's territory.
func TestNewTokenMiddleware_FailsClosedAndAcceptsValidToken(t *testing.T) {
	dir := t.TempDir()
	tf := filepath.Join(dir, "token")
	if err := os.WriteFile(tf, nil, 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	resetTokenCache(t, tf)

	fiberApp := fiber.New()
	fiberApp.Use(NewTokenMiddleware())
	fiberApp.Get("/ping", func(c *fiber.Ctx) error { return c.SendStatus(fiber.StatusOK) })

	doReq := func(authHeader string) int {
		t.Helper()
		req := httptest.NewRequest("GET", "/ping", nil)
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
		resp, err := fiberApp.Test(req)
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	// Empty token file: a request with no Authorization header at all must
	// still be rejected -- this was the auth-bypass defect (empty token
	// file previously accepted an empty parsed token).
	if code := doReq(""); code != fiber.StatusUnauthorized {
		t.Fatalf("empty token file, no header: expected 401, got %d", code)
	}

	// Provision a real token; the middleware must pick it up (rotation)
	// without any restart or cache reset.
	if err := os.WriteFile(tf, []byte("good-token"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	if code := doReq("Bearer wrong-token"); code != fiber.StatusUnauthorized {
		t.Fatalf("wrong token: expected 401, got %d", code)
	}
	if code := doReq("Bearer good-token"); code != fiber.StatusOK {
		t.Fatalf("correct token: expected 200, got %d", code)
	}
}
