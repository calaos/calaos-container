package models

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		// URL userinfo.
		{"GET http://admin:s3cr3t@192.168.1.20/snap.jpg failed", "GET http://***@192.168.1.20/snap.jpg failed"},
		{"rtsp://user:p@ss@cam/stream", "rtsp://***@ss@cam/stream"},
		// Query credentials.
		{"fetch http://cam/cgi?loginuser=bob&res=1&passwd2=x", "fetch http://cam/cgi?loginuser=***&res=1&passwd2=***"},
		{"url /snapshot.cgi?sid=1&authcode=x&fmt=jpg", "url /snapshot.cgi?sid=1&authcode=***&fmt=jpg"},
		// A listed key in a query string: rule 3 then masks the rest of it.
		{"fetch http://cam/cgi?user=bob&pwd=hunter2&res=1", "fetch http://cam/cgi?user=***&pwd=***"},
		{"/api?cn_pass=abc def", "/api?cn_pass=*** def"},
		{"/x?access_token=t1&mySessionId=9&n=2", "/x?access_token=***&mySessionId=***&n=2"},
		// key=value and key: value.
		{"login cn_user=admin cn_pass=topsecret ok", "login cn_user=*** cn_pass=*** ok"},
		{"password: hunter2", "password=***"},
		{"SMTP_PASSWORD = 'a b c' sent", "SMTP_PASSWORD=*** sent"},
		{"mcp_token=abc", "mcp_token=***"},
		{"x-calaos-admin-session: 0123abcd", "x-calaos-admin-session=***"},
		{"psk=wifipass private_key=k", "psk=*** private_key=***"},
		// JSON form.
		{`{"cn_pass": "x y", "port": 5454}`, `{"cn_pass=***, "port": 5454}`},
		{`{"influxdb_token":"tok","host":"h"}`, `{"influxdb_token=***,"host":"h"}`},
		{`{'push_key': 'k'}`, `{'push_key=***}`},
		// Bearer.
		{"Authorization: Bearer eyJhbGci.x.y", "authorization: bearer ***"},
		{"header authorization:bearer abc next", "header authorization: bearer *** next"},
		// Push code text.
		{"push: Calaos admin code 123456 for console", "push: Calaos admin code ****** for console"},
		{"code: 654321", "code=***"},
	} {
		if got := Redact(tc.in); got != tc.want {
			t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRedactKeepsHarmlessLines(t *testing.T) {
	for _, in := range []string{
		"wago: connected to 192.168.1.50:502",
		"pam_unix(sshd:session): session opened for user root(uid=0)",
		"http://cam.local:8080/snap.jpg",
		"compass heading 12",
		"Calaos admin code requested",
		"https://example.org/path?page=2&sort=asc",
		"bypass mode on",
	} {
		if got := Redact(in); got != in {
			t.Errorf("Redact(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestRedactNeverLeaves(t *testing.T) {
	secret := "Zq9-SECRET-value" // #nosec G101 -- test corpus
	for _, in := range []string{
		"http://u:" + secret + "@h/",
		"?token=" + secret,
		"&auth=" + secret,
		"password=" + secret,
		`"calaos_password": "` + secret + `"`,
		"admin_pass:" + secret,
		"new_pass = " + secret,
		"Authorization: Bearer " + secret,
		"apikey " + "= " + secret,
	} {
		if got := Redact(in); strings.Contains(got, secret) {
			t.Errorf("Redact(%q) = %q leaks the secret", in, got)
		}
	}
}
