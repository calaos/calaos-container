package models

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		// URL userinfo.
		{"GET http://admin:s3cr3t@192.168.1.20/snap.jpg failed", "GET http://***@192.168.1.20/snap.jpg failed"},
		{"rtsp://user:p@ss@cam/stream", "rtsp://***@cam/stream"},
		{"http://cam.local:8080/snap.jpg?x=1", "http://cam.local:8080/snap.jpg?x=1"},
		// Query credentials.
		{"fetch http://cam/cgi?loginuser=bob&res=1&passwd2=x", "fetch http://cam/cgi?loginuser=***&res=1&passwd2=***"},
		{"url /snapshot.cgi?sid=1&authcode=x&fmt=jpg", "url /snapshot.cgi?sid=1&authcode=***&fmt=jpg"},
		// A listed key in a query string: rule 3 then masks the rest of it.
		{"fetch http://cam/cgi?user=bob&pwd=hunter2&res=1", "fetch http://cam/cgi?user=***&pwd=***"},
		{"/api?cn_pass=abc def", "/api?cn_pass=*** def"},
		{"/x?mySessionId=9&n=2", "/x?mySessionId=***&n=2"},
		{"/x?access_token=t1&n=2", "/x?access_token=***"},
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
		{"Authorization: Bearer eyJhbGci.x.y", "Authorization: Bearer ***"},
		{"header authorization:bearer abc next\nHost: h", "header authorization:bearer ***\nHost: h"},
		{"Proxy-Authorization: Digest username=\"a\", response=\"b\"", "Proxy-Authorization: Digest ***"},
		{"Set-Cookie: sid=abc; Path=/", "Set-Cookie: ***"},
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
		"zigbee key: 0x00124b00",
		"monkey: 3",
		"keySize=2048 ok",
		"ssh://git@github.com:22/repo",
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

// Lines a Calaos box can plausibly write, from the security review.
func TestRedactCorpus(t *testing.T) {
	const secret = "Zq9SECRETvalue" // #nosec G101 -- test corpus
	for _, in := range []string{
		"login user_password=" + secret + " ok",
		"mosquitto: mqtt_password=" + secret,
		`{"push_token": "` + secret + `"}`,
		"registered device_token=" + secret,
		"oauth access_token=" + secret,
		"network_key=" + secret,
		"wifi-psk=" + secret,
		"client_secret: " + secret,
		"refresh_token: " + secret,
		"adminPassword=" + secret,
		`{"apiToken":"` + secret + `"}`,
		"GET rtsp://me@x.com:" + secret + "@cam/stream",
		"GET http://admin:Zq9/" + secret + "@cam/snap.jpg",
		"rtsp://user:Zq@" + secret + "@cam/stream",
		"Authorization: Basic " + secret,
		"Proxy-Authorization: Basic " + secret,
		"Cookie: admin_session_id=" + secret + "; other=1",
		"Set-Cookie: sid=" + secret + "; Path=/",
		"GET /api;token=" + secret,
		"\"cn_pass\"\t:\t\"" + secret + "\"",
	} {
		if got := Redact(in); strings.Contains(got, secret) {
			t.Errorf("Redact(%q) = %q leaks the secret", in, got)
		}
	}

	// Known gaps, out of reach of a line-based table: SMTP AUTH payloads,
	// keys in a URL path, unlisted parameter names, values with a blank.
	for _, in := range []string{
		"> AUTH PLAIN " + secret,
		"GET http://192.168.1.5/api/" + secret + "/lights",
		"GET /cgi-bin/snap?pw=" + secret + "&ch=1",
		"password=Zq9 " + secret,
		"password " + secret,
		"pwd%3D" + secret,
	} {
		if got := Redact(in); strings.Contains(got, secret) {
			t.Logf("known gap: %q", got)
		}
	}
}
