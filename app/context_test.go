package app

import (
	"crypto/rand"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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

func TestTokenEqual(t *testing.T) {
	const want = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cases := []struct {
		got, expected string
		ok            bool
	}{
		{want, want, true},
		{"", want, false},
		{want[:63], want, false},
		{want + "0", want, false},
		{strings.ToUpper(want), want, false},
		{"", "", false},
		{"x", "", false},
	}
	for _, c := range cases {
		if got := tokenEqual(c.got, c.expected); got != c.ok {
			t.Errorf("tokenEqual(%q, %q) = %v, want %v", c.got, c.expected, got, c.ok)
		}
	}
}

// TestTokenComparedInConstantTime fails if context.go compares a token with
// == or != (other than against a literal) or if tokenEqual stops using
// subtle.ConstantTimeCompare: timing cannot be asserted reliably at run time.
func TestTokenComparedInConstantTime(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "context.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	secret := map[string]bool{"token": true, "expected": true, "got": true, "tokenStr": true, "cached": true, "trimmed": true}
	isSecret := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && secret[id.Name]
	}
	usesSubtle := false
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BinaryExpr:
			if n.Op != token.EQL && n.Op != token.NEQ {
				return true
			}
			_, xLit := n.X.(*ast.BasicLit)
			_, yLit := n.Y.(*ast.BasicLit)
			if (isSecret(n.X) && !yLit) || (isSecret(n.Y) && !xLit) {
				t.Errorf("%s: token compared with %s", fset.Position(n.Pos()), n.Op)
			}
		case *ast.FuncDecl:
			if n.Name.Name != "tokenEqual" {
				return true
			}
			ast.Inspect(n.Body, func(m ast.Node) bool {
				if sel, ok := m.(*ast.SelectorExpr); ok && sel.Sel.Name == "ConstantTimeCompare" {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "subtle" {
						usesSubtle = true
					}
				}
				return true
			})
		}
		return true
	})
	if !usesSubtle {
		t.Error("tokenEqual no longer calls subtle.ConstantTimeCompare")
	}
}

func TestIsLegacyEventsPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/api/events":      true,
		"/API/Events":      true,
		"/api/events/":     true,
		"/api/v2/events":   false,
		"/api/events/x":    false,
		"/api/eventsx":     false,
		"/events":          false,
		"/api/update/info": false,
	} {
		if got := isLegacyEventsPath(p); got != want {
			t.Errorf("isLegacyEventsPath(%q) = %v, want %v", p, got, want)
		}
	}
}

// testRoutePath turns a registered route into a concrete request path.
func testRoutePath(p string) string {
	return strings.NewReplacer(":intf", "eth0", ":code_id", "0123456789abcdef").Replace(p)
}

// TestAuthQueryTokenOnlyOnLegacyEvents walks every registered route, so a
// route added later is covered without touching this test.
func TestAuthQueryTokenOnlyOnLegacyEvents(t *testing.T) {
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)

	seen := 0
	for _, r := range a.appFiber.GetRoutes(true) {
		if (r.Method != "GET" && r.Method != "POST") || !strings.HasPrefix(r.Path, "/api/") || isLegacyEventsPath(r.Path) {
			continue
		}
		seen++
		tc := httpCase{method: r.Method, path: testRoutePath(r.Path) + "?token=" + testToken, auth: "-"}
		resp, body := doCase(t, a, tc)
		if resp.StatusCode != fiber.StatusUnauthorized {
			t.Errorf("%s %s with ?token=: got %d %s, want 401", r.Method, tc.path, resp.StatusCode, body)
			continue
		}
		want := `{"error":true,"msg":"wrong token"}`
		if isV2Path(r.Path) {
			want = `{"error":true,"code":"bad_bearer","msg":"unauthorized"}`
		}
		if string(body) != want {
			t.Errorf("%s %s: body %s, want %s", r.Method, tc.path, body, want)
		}

		// Control: the same route with the header gets past the middleware.
		// Session-only routes then ask for the session they lack.
		resp, body = doCase(t, a, httpCase{method: r.Method, path: testRoutePath(r.Path)})
		if resp.StatusCode == fiber.StatusUnauthorized && !strings.Contains(string(body), `"code":"bad_session"`) {
			t.Errorf("%s %s with the header: got 401 %s", r.Method, r.Path, body)
		}
	}
	if seen < 20 {
		t.Fatalf("only %d routes checked, route listing changed?", seen)
	}

	for _, p := range []string{"/api/events", "/API/EVENTS", "/api/events/"} {
		resp, body := doCase(t, a, httpCase{method: "GET", path: p + "?token=" + testToken, auth: "-"})
		if resp.StatusCode != fiber.StatusUpgradeRequired {
			t.Errorf("GET %s?token=: got %d %s, want 426 past auth", p, resp.StatusCode, body)
		}
	}
	resp, _ := doCase(t, a, httpCase{method: "GET", path: "/api/events?token=nope", auth: "-"})
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Errorf("wrong ?token= on /api/events: got %d, want 401", resp.StatusCode)
	}
}

// TestAuthEmptyTokenRefused: with no usable token every way of presenting an
// empty or arbitrary credential is refused, with a body that names no path
// or OS error.
func TestAuthEmptyTokenRefused(t *testing.T) {
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)
	dir := t.TempDir()

	files := map[string]string{"missing": filepath.Join(dir, "missing")}
	for name, content := range map[string]string{"empty": "", "whitespace": " \n\t\n"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		files[name] = p
	}
	unreadable := filepath.Join(dir, "unreadable")
	if err := os.Mkdir(unreadable, 0o700); err != nil {
		t.Fatal(err)
	}
	files["unreadable"] = unreadable // a directory: stat works, read fails

	reqs := []httpCase{
		{method: "GET", path: "/api/update/available", auth: "-"},
		{method: "GET", path: "/api/update/available", auth: "Bearer "},
		{method: "GET", path: "/api/update/available", auth: "Bearer"},
		{method: "GET", path: "/api/events?token=", auth: "-"},
		{method: "GET", path: "/api/events?token=x", auth: "-"},
		{method: "GET", path: "/api/v2/update/available", auth: "-"},
		{method: "GET", path: "/api/v2/update/available", auth: "Bearer "},
	}
	for name, p := range files {
		t.Run(name, func(t *testing.T) {
			resetTokenCache(t, p)
			for _, tc := range reqs {
				resp, body := doCase(t, a, tc)
				want := `{"error":true,"msg":"unauthorized"}`
				if isV2Path(tc.path) {
					want = `{"error":true,"code":"bad_bearer","msg":"unauthorized"}`
				}
				if resp.StatusCode != fiber.StatusUnauthorized || string(body) != want {
					t.Errorf("%s %s (auth %q): got %d %s, want 401 %s", tc.method, tc.path, tc.auth, resp.StatusCode, body, want)
				}
			}
		})
	}
}

// TestAuthTokenFormats: the 64-hex token init_calaosfs writes works raw in a
// URL (old kiosks do not percent-encode it), and the weak token an upgraded
// box keeps until its next boot is still accepted.
func TestAuthTokenFormats(t *testing.T) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{
		"generated":   hex.EncodeToString(raw),
		"legacy_weak": "1729000000-123456789-12345",
	} {
		t.Run(name, func(t *testing.T) {
			(&stubBackend{}).install(t)
			a := newHandlerTestApp(t)
			tf := filepath.Join(t.TempDir(), "calaos-ct.token")
			if err := os.WriteFile(tf, []byte(tok+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			resetTokenCache(t, tf)

			if name == "generated" && !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(tok) {
				t.Fatalf("token %q is not 64 lowercase hex chars", tok)
			}
			resp, body := doCase(t, a, httpCase{method: "GET", path: "/api/update/available", auth: "bearer " + tok})
			if resp.StatusCode != fiber.StatusOK {
				t.Fatalf("header: got %d %s", resp.StatusCode, body)
			}
			resp, body = doCase(t, a, httpCase{method: "GET", path: "/api/events?token=" + tok, auth: "-"})
			if resp.StatusCode != fiber.StatusUpgradeRequired {
				t.Fatalf("query on /api/events: got %d %s", resp.StatusCode, body)
			}
		})
	}
}

// TestAuthConcurrentFirstLoad runs under -race in CI: many requests race on
// the empty cache, then on a rotation.
func TestAuthConcurrentFirstLoad(t *testing.T) {
	tf := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tf, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resetTokenCache(t, tf)

	fiberApp := fiber.New()
	fiberApp.Use(NewTokenMiddleware())
	fiberApp.Get("/ping", func(c *fiber.Ctx) error { return c.SendStatus(fiber.StatusOK) })

	hammer := func(tok string, want int) {
		t.Helper()
		var wg sync.WaitGroup
		errs := make(chan string, 64)
		for i := 0; i < 64; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				req := httptest.NewRequest("GET", "/ping", nil)
				req.Header.Set("Authorization", "Bearer "+tok)
				resp, err := fiberApp.Test(req, -1)
				if err != nil {
					errs <- err.Error()
					return
				}
				_ = resp.Body.Close()
				if resp.StatusCode != want {
					errs <- http.StatusText(resp.StatusCode)
				}
			}()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			t.Errorf("token %q: %s, want %d", tok, e, want)
		}
	}

	hammer("first", fiber.StatusOK)
	if err := os.WriteFile(tf, []byte("second-rotated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hammer("first", fiber.StatusUnauthorized)
	hammer("second-rotated", fiber.StatusOK)
}
