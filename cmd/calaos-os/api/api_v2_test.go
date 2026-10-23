package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/calaos/calaos-container/models/structs"
)

// newFakeDaemon serves the given routes; anything else gets a plain-text 404
// like a daemon without /api/v2.
func newFakeDaemon(t *testing.T, routes map[string]func(w http.ResponseWriter, r *http.Request)) *CalaosApi {
	t.Helper()
	mux := http.NewServeMux()
	for p, h := range routes {
		mux.HandleFunc(p, h)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return NewCalaosApi(strings.TrimPrefix(srv.URL, "http://"))
}

func reply(status int, body string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func TestV2EnvelopeDecoded(t *testing.T) {
	capi := newFakeDaemon(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v2/update/available": reply(200, `{"error":false,"msg":"ok","data":{"calaos-server":{"name":"calaos-server","version":"2","current_version":"1"}}}`),
		"/api/v2/update/status":    reply(200, `{"error":false,"msg":"ok","data":{"progress":{"status":"upgrading","total_percent":40},"job":null}}`),
		"/api/v2/network/list":     reply(200, `{"error":false,"msg":"ok","data":[{"name":"eth0","ipv4":"10.0.0.2/24","dhcp":true}]}`),
		"/api/v2/network/eth0":     reply(200, `{"error":false,"msg":"ok","data":null}`),
	})

	imgs, err := capi.UpdateAvailable("tok")
	if err != nil || (*imgs)["calaos-server"].Version != "2" {
		t.Fatalf("UpdateAvailable: %v %v", imgs, err)
	}
	st, err := capi.UpgradeStatus("tok")
	if err != nil || st.Status != "upgrading" || st.TotalPercent != 40 {
		t.Fatalf("UpgradeStatus: %+v %v", st, err)
	}
	nets, err := capi.GetNetworkInterfaces("tok")
	if err != nil || len(*nets) != 1 || (*nets)[0].Name != "eth0" {
		t.Fatalf("GetNetworkInterfaces: %v %v", nets, err)
	}
	if err := capi.ConfigureNetworkInterface("tok", "eth0", &structs.NetInterface{DHCP: true}); err != nil {
		t.Fatalf("ConfigureNetworkInterface: %v", err)
	}
}

func TestV2ErrorCarriesCode(t *testing.T) {
	capi := newFakeDaemon(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v2/update/check": reply(409, `{"error":true,"code":"upgrade_in_progress","msg":"an upgrade is already in progress"}`),
		"/api/v2/network/eth0": reply(400, `{"error":true,"code":"invalid_request","msg":"invalid ipv4"}`),
	})

	_, err := capi.UpdateCheck("tok")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 409 || ae.Code != "upgrade_in_progress" {
		t.Fatalf("UpdateCheck error %v", err)
	}

	err = capi.ConfigureNetworkInterface("tok", "eth0", &structs.NetInterface{})
	if !errors.As(err, &ae) || ae.Code != "invalid_request" || err.Error() != "ConfigureNetworkInterface failed: invalid ipv4" {
		t.Fatalf("ConfigureNetworkInterface error %v", err)
	}
}

// TestLegacyFallback: the new CLI keeps working against the old daemon still
// in memory right after a package upgrade.
func TestLegacyFallback(t *testing.T) {
	capi := newFakeDaemon(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/update/check":  reply(200, `{"calaos-server":{"name":"calaos-server","version":"2","current_version":"1"}}`),
		"/api/update/status": reply(200, `{"status":"idle","total_percent":0}`),
		"/api/network/list":  reply(200, `{"error":false,"msg":"ok","output":[{"name":"eth0","dhcp":true}]}`),
		"/api/network/eth0":  reply(400, `{"error":true,"msg":"invalid interface: \"eth0\" does not exist"}`),
	})

	imgs, err := capi.UpdateCheck("tok")
	if err != nil || len(*imgs) != 1 {
		t.Fatalf("UpdateCheck: %v %v", imgs, err)
	}
	st, err := capi.UpgradeStatus("tok")
	if err != nil || st.Status != "idle" {
		t.Fatalf("UpgradeStatus: %+v %v", st, err)
	}
	nets, err := capi.GetNetworkInterfaces("tok")
	if err != nil || len(*nets) != 1 {
		t.Fatalf("GetNetworkInterfaces: %v %v", nets, err)
	}
	err = capi.ConfigureNetworkInterface("tok", "eth0", &structs.NetInterface{DHCP: true})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("ConfigureNetworkInterface: %v", err)
	}
}

func TestLegacyFallbackSurfacesErrors(t *testing.T) {
	capi := newFakeDaemon(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/update/check": reply(500, `{"error":true,"msg":"upgrade already in progress"}`),
	})

	_, err := capi.UpdateCheck("tok")
	if err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("UpdateCheck: %v", err)
	}
}

func TestBearerErrorOnV2(t *testing.T) {
	capi := newFakeDaemon(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v2/update/available": reply(401, `{"error":true,"code":"bad_bearer","msg":"unauthorized"}`),
	})

	_, err := capi.UpdateAvailable("bad")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 401 || ae.Code != "bad_bearer" {
		t.Fatalf("UpdateAvailable error %v", err)
	}
}

func TestEventsWSPrefersV2(t *testing.T) {
	var hit string
	upgrader := websocket.Upgrader{}
	handler := func(path string) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			hit = path
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			_, _, _ = conn.ReadMessage()
		}
	}
	capi := newFakeDaemon(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v2/events": handler("v2"),
		"/api/events":    handler("legacy"),
	})

	conn, err := capi.EventsWS("tok")
	if err != nil {
		t.Fatalf("EventsWS: %v", err)
	}
	_ = conn.Close()
	if hit != "v2" {
		t.Fatalf("dialed %q, want v2", hit)
	}
}

// TestEnvelopedNotFoundDoesNotFallBack: only a daemon without /api/v2 (plain
// 404) sends the CLI back to the legacy routes.
func TestEnvelopedNotFoundDoesNotFallBack(t *testing.T) {
	capi := newFakeDaemon(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v2/update/check": reply(404, `{"error":true,"code":"not_found","msg":"not found"}`),
		"/api/update/check": func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("legacy route hit")
		},
	})

	_, err := capi.UpdateCheck("tok")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 404 || ae.Code != "not_found" {
		t.Fatalf("UpdateCheck error %v", err)
	}
}

func TestEventsWSFallsBackToLegacy(t *testing.T) {
	hit := ""
	upgrader := websocket.Upgrader{}
	capi := newFakeDaemon(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/events": func(w http.ResponseWriter, r *http.Request) {
			hit = "legacy"
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			_, _, _ = conn.ReadMessage()
		},
	})

	conn, err := capi.EventsWS("tok")
	if err != nil {
		t.Fatalf("EventsWS: %v", err)
	}
	_ = conn.Close()
	if hit != "legacy" {
		t.Fatalf("dialed %q, want legacy", hit)
	}
}

func TestStartUpgradeAndJobStatus(t *testing.T) {
	var body string
	capi := newFakeDaemon(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v2/update/upgrade": func(w http.ResponseWriter, r *http.Request) {
			b := new(strings.Builder)
			_, _ = io.Copy(b, r.Body)
			body = b.String()
			reply(202, `{"error":false,"msg":"accepted","data":{"id":"3f9c2a7e5b1d4c80","kind":"upgrade","package":"calaos-server","status":"running","reboot_required":false,"daemon_restart":false,"started_at":"2026-10-22T09:12:03Z"}}`)(w, r)
		},
		"/api/v2/update/status": reply(200, `{"error":false,"msg":"ok","data":{"progress":{"status":"idle"},"job":{"id":"3f9c2a7e5b1d4c80","status":"done","reboot_required":true,"finished_at":"2026-10-22T09:20:00Z"}}}`),
	})

	job, err := capi.StartUpgrade("tok", "calaos-server")
	if err != nil || job.ID != "3f9c2a7e5b1d4c80" || job.Package != "calaos-server" || job.Terminal() {
		t.Fatalf("StartUpgrade: %+v %v", job, err)
	}
	if body != `{"package":"calaos-server"}` {
		t.Fatalf("request body %q", body)
	}
	job, err = capi.JobStatus("tok")
	if err != nil || !job.Terminal() || !job.RebootRequired {
		t.Fatalf("JobStatus: %+v %v", job, err)
	}
}

func TestStartUpgradeErrors(t *testing.T) {
	capi := newFakeDaemon(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v2/update/upgrade-all": reply(409, `{"error":true,"code":"upgrade_in_progress","msg":"an upgrade is already in progress","data":{"job_id":"3f9c2a7e5b1d4c80"}}`),
		"/api/v2/update/upgrade":     reply(202, `{"error":false,"msg":"accepted","data":null}`),
	})

	_, err := capi.StartUpgrade("tok", "")
	if InProgressJobID(err) != "3f9c2a7e5b1d4c80" {
		t.Fatalf("409: %v", err)
	}
	if _, err := capi.StartUpgrade("tok", "calaos-server"); err == nil {
		t.Fatal("an answer without a job was accepted")
	}
	if InProgressJobID(errors.New("x")) != "" {
		t.Fatal("job id from a plain error")
	}

	old := newFakeDaemon(t, nil)
	if _, err := old.StartUpgrade("tok", ""); !errors.Is(err, ErrNoJobs) {
		t.Fatalf("daemon without /api/v2: %v", err)
	}
}
