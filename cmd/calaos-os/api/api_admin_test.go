package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
)

func TestAdminSetRecovery(t *testing.T) {
	var got struct {
		method, query, auth string
		body                map[string]any
	}
	capi := newFakeDaemon(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v2/admin/set": func(w http.ResponseWriter, r *http.Request) {
			got.method, got.query, got.auth = r.Method, r.URL.RawQuery, r.Header.Get("Authorization")
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &got.body)
			reply(200, `{"error":false,"msg":"ok","data":{"mode":"recovery"}}`)(w, r)
		},
	})

	mode, err := capi.AdminSet("tok", "correct horse battery")
	if err != nil || mode != "recovery" {
		t.Fatalf("AdminSet: %q %v", mode, err)
	}
	if got.method != "POST" || got.query != "" || got.auth != "Bearer tok" {
		t.Fatalf("request %+v", got)
	}
	if len(got.body) != 2 || got.body["password"] != "correct horse battery" || got.body["recovery"] != true {
		t.Fatalf("body %v", got.body)
	}
}

func TestAdminErrors(t *testing.T) {
	capi := newFakeDaemon(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v2/admin/set": reply(400, `{"error":true,"code":"password_too_short","msg":"the admin password is too short","data":{"min_length":10}}`),
	})
	var ae *APIError
	if _, err := capi.AdminSet("tok", "short"); !errors.As(err, &ae) || ae.Code != "password_too_short" {
		t.Fatalf("too short: %v", err)
	}

	// Old daemon: no /api/v2 at all, or /api/v2 without the admin routes.
	old := newFakeDaemon(t, nil)
	if _, err := old.AdminSet("tok", "correct horse battery"); !errors.Is(err, ErrNoAdmin) {
		t.Fatalf("daemon without /api/v2: %v", err)
	}
	noAdmin := newFakeDaemon(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v2/": reply(404, `{"error":true,"code":"not_found","msg":"not found"}`),
	})
	if _, err := noAdmin.AdminReset("tok"); !errors.Is(err, ErrNoAdmin) {
		t.Fatalf("daemon without admin routes: %v", err)
	}
}

func TestAdminReset(t *testing.T) {
	var method string
	capi := newFakeDaemon(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v2/admin/reset": func(w http.ResponseWriter, r *http.Request) {
			method = r.Method
			reply(200, `{"error":false,"msg":"ok","data":{"was_set":true}}`)(w, r)
		},
	})
	if was, err := capi.AdminReset("tok"); !was || err != nil || method != "POST" {
		t.Fatalf("AdminReset: %v %v %s", was, err, method)
	}
}
