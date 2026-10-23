package api

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/calaos/calaos-container/models/structs"
)

const (
	CalaosCtHost = "localhost:8000"

	apiV2 = "/api/v2"

	// DefaultTimeout bounds the http.Client used for non-streaming REST
	// calls (list/check/upgrade/network) so a hung or unreachable daemon
	// does not block the CLI indefinitely. Callers can override it via
	// SetTimeout. EventsWS uses its own WebSocket dialer/handshake timeout
	// and is unaffected by this value.
	DefaultTimeout = 30 * time.Second
)

type apiOptions struct {
	timeout   time.Duration
	insecure  bool
	proxy     func(*http.Request) (*url.URL, error)
	transport func(*http.Transport)
}

// ApiResponse is the body of the legacy (unprefixed) system and network
// routes, used only when the daemon predates /api/v2.
type ApiResponse struct {
	Error  bool            `json:"error"`
	Msg    string          `json:"msg"`
	Output json.RawMessage `json:"output"`
}

// Envelope is the body of every /api/v2 answer (docs/API.md).
type Envelope struct {
	Error bool            `json:"error"`
	Code  string          `json:"code"`
	Msg   string          `json:"msg"`
	Data  json.RawMessage `json:"data"`
}

// APIError is an error answer of the daemon. Code is set by /api/v2 only.
type APIError struct {
	Status int
	Code   string
	Msg    string
}

func (e *APIError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return fmt.Sprintf("api error: %d %s", e.Status, http.StatusText(e.Status))
}

// errNoV2 means the daemon in memory predates /api/v2: right after a package
// upgrade, the new CLI runs against the old daemon until it restarts.
var errNoV2 = errors.New("daemon does not serve /api/v2")

// Optional parameter, used to configure timeouts on API calls.
func SetTimeout(timeout time.Duration) func(*apiOptions) {
	return func(opts *apiOptions) {
		opts.timeout = timeout
	}
}

// Optional parameter for testing only.  Bypasses all TLS certificate validation.
func SetInsecure() func(*apiOptions) {
	return func(opts *apiOptions) {
		opts.insecure = true
	}
}

// Optional parameter, used to configure an HTTP Connect proxy
// server for all outbound communications.
func SetProxy(proxy func(*http.Request) (*url.URL, error)) func(*apiOptions) {
	return func(opts *apiOptions) {
		opts.proxy = proxy
	}
}

// SetTransport enables additional control over the HTTP transport used to connect to the API.
func SetTransport(transport func(*http.Transport)) func(*apiOptions) {
	return func(opts *apiOptions) {
		opts.transport = transport
	}
}

type CalaosApi struct {
	host      string
	userAgent string
	apiClient *http.Client
}

// SetCustomHTTPClient allows one to set a completely custom http client that
// will be used to make network calls to the duo api
func (capi *CalaosApi) SetCustomHTTPClient(c *http.Client) {
	capi.apiClient = c
}

// Build and return a CalaosApi struct
func NewCalaosApi(host string, options ...func(*apiOptions)) *CalaosApi {
	opts := apiOptions{
		proxy:   http.ProxyFromEnvironment,
		timeout: DefaultTimeout,
	}
	for _, o := range options {
		o(&opts)
	}

	tr := &http.Transport{
		Proxy: opts.proxy,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: opts.insecure,
		},
	}
	if opts.transport != nil {
		opts.transport(tr)
	}

	return &CalaosApi{
		host:      host,
		userAgent: "calaos-os_api/1.0",
		apiClient: &http.Client{
			Timeout:   opts.timeout,
			Transport: tr,
		},
	}
}

// Make a CalaosApi Rest API call
// Example: api.Call("POST", "/xxxxx/xxxxx", nil)
func (capi *CalaosApi) call(method string, uri string, params url.Values, body interface{}) (*http.Response, []byte, error) {

	url := url.URL{
		Scheme:   "http",
		Host:     capi.host,
		Path:     uri,
		RawQuery: params.Encode(),
	}
	headers := make(map[string]string)
	headers["User-Agent"] = capi.userAgent

	var requestBody io.ReadCloser = nil
	if body != nil {
		headers["Content-Type"] = "application/json"

		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		requestBody = io.NopCloser(bytes.NewReader(b))
	}

	return capi.makeHttpCall(method, url, headers, requestBody)
}

// Make a CalaosApi Rest API call using token
// Example: api.CallWithToken("GET", "/api/xxxx", token, params)
func (capi *CalaosApi) callWithToken(method string, uri string, token string, params url.Values, body interface{}) (*http.Response, []byte, error) {

	url := url.URL{
		Scheme:   "http",
		Host:     capi.host,
		Path:     uri,
		RawQuery: params.Encode(),
	}
	headers := make(map[string]string)
	headers["User-Agent"] = capi.userAgent
	headers["Authorization"] = "Bearer " + token

	var requestBody io.ReadCloser = nil
	if body != nil {
		headers["Content-Type"] = "application/json"

		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		requestBody = io.NopCloser(bytes.NewReader(b))
	}

	return capi.makeHttpCall(method, url, headers, requestBody)
}

func (capi *CalaosApi) makeHttpCall(
	method string,
	url url.URL,
	headers map[string]string,
	body io.ReadCloser) (*http.Response, []byte, error) {

	request, err := http.NewRequest(method, url.String(), nil)
	if err != nil {
		return nil, nil, err
	}

	//set headers
	for k, v := range headers {
		request.Header.Set(k, v)
	}

	if body != nil {
		request.Body = body
	}

	resp, err := capi.apiClient.Do(request)
	var bodyBytes []byte
	if err != nil {
		return resp, bodyBytes, err
	}

	bodyBytes, err = io.ReadAll(resp.Body)
	defer resp.Body.Close()

	return resp, bodyBytes, err
}

// callV2 calls an /api/v2 route and decodes the envelope data into out.
func (capi *CalaosApi) callV2(token, method, path string, body, out any) error {
	resp, b, err := capi.callWithToken(method, apiV2+path, token, nil, body)
	if err != nil {
		return err
	}

	var env Envelope
	if jerr := json.Unmarshal(b, &env); jerr != nil || (resp.StatusCode == http.StatusNotFound && env.Code == "") {
		if resp.StatusCode == http.StatusNotFound {
			return errNoV2
		}
		return fmt.Errorf("invalid daemon answer (%s)", resp.Status)
	}

	if env.Error || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Code: env.Code, Msg: env.Msg}
	}

	if out != nil {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("invalid daemon answer: %w", err)
		}
	}
	return nil
}

// legacyGetJSON reads a legacy route that answers a raw JSON document.
func (capi *CalaosApi) legacyGetJSON(token, path string, out any) error {
	resp, body, err := capi.callWithToken("GET", path, token, nil, nil)
	if err != nil {
		return err
	}
	if err := apiError(resp, body); err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// UpdateCheck forces an update check
func (capi *CalaosApi) UpdateCheck(token string) (*structs.ImageMap, error) {
	imgs := &structs.ImageMap{}
	err := capi.callV2(token, "GET", "/update/check", nil, imgs)
	if errors.Is(err, errNoV2) {
		err = capi.legacyGetJSON(token, "/api/update/check", imgs)
	}
	if err != nil {
		return nil, fmt.Errorf("UpdateCheck failed: %w", err)
	}
	return imgs, nil
}

// UpdateAvailable returns available updates
func (capi *CalaosApi) UpdateAvailable(token string) (*structs.ImageMap, error) {
	imgs := &structs.ImageMap{}
	err := capi.callV2(token, "GET", "/update/available", nil, imgs)
	if errors.Is(err, errNoV2) {
		err = capi.legacyGetJSON(token, "/api/update/available", imgs)
	}
	if err != nil {
		return nil, fmt.Errorf("UpdateAvailable failed: %w", err)
	}
	return imgs, nil
}

// apiError converts a non-2xx legacy reply ({"error":true,"msg":"..."}) into
// a Go error. makeHttpCall only fails on transport errors, so HTTP-level
// failures (e.g. 500 "package not found") must be surfaced explicitly.
func apiError(resp *http.Response, body []byte) error {
	if resp == nil {
		return fmt.Errorf("api error: no response")
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	var ar ApiResponse
	if err := json.Unmarshal(body, &ar); err == nil && ar.Msg != "" {
		return &APIError{Status: resp.StatusCode, Msg: ar.Msg}
	}
	return &APIError{Status: resp.StatusCode}
}

// UpgradePackages upgrades all packages. Upgrades stay on the legacy
// synchronous route until /api/v2 serves them as jobs.
func (capi *CalaosApi) UpgradePackages(token string) (err error) {
	resp, body, err := capi.callWithToken("POST", "/api/update/upgrade-all", token, nil, nil)
	if err != nil {
		return
	}

	return apiError(resp, body)
}

// UpdatePackage upgrades a single package, on the legacy route like
// UpgradePackages.
func (capi *CalaosApi) UpdatePackage(token string, pkg string) (err error) {
	params := url.Values{
		"package": []string{pkg},
	}

	resp, body, err := capi.callWithToken("POST", "/api/update/upgrade", token, params, nil)
	if err != nil {
		return
	}

	return apiError(resp, body)
}

// UpgradeStatus returns the status of the upgrade
func (capi *CalaosApi) UpgradeStatus(token string) (*structs.Status, error) {
	var data struct {
		Progress *structs.Status `json:"progress"`
	}
	err := capi.callV2(token, "GET", "/update/status", nil, &data)
	if errors.Is(err, errNoV2) {
		data.Progress = &structs.Status{}
		err = capi.legacyGetJSON(token, "/api/update/status", data.Progress)
	}
	if err != nil {
		return nil, fmt.Errorf("UpgradeStatus failed: %w", err)
	}
	if data.Progress == nil {
		return nil, fmt.Errorf("UpgradeStatus failed: no progress in the answer")
	}
	return data.Progress, nil
}

// GetNetworkInterfaces returns a list of network interfaces with their status
func (capi *CalaosApi) GetNetworkInterfaces(token string) (*[]structs.NetInterface, error) {
	netf := &[]structs.NetInterface{}
	err := capi.callV2(token, "GET", "/network/list", nil, netf)
	if errors.Is(err, errNoV2) {
		err = capi.legacyCall(token, "GET", "/api/network/list", nil, netf)
	}
	if err != nil {
		return nil, fmt.Errorf("GetNetworkInterfaces failed: %w", err)
	}
	return netf, nil
}

func (capi *CalaosApi) ConfigureNetworkInterface(token string, intf string, config *structs.NetInterface) error {
	err := capi.callV2(token, "POST", "/network/"+intf, config, nil)
	if errors.Is(err, errNoV2) {
		err = capi.legacyCall(token, "POST", "/api/network/"+intf, config, nil)
	}
	if err != nil {
		return fmt.Errorf("ConfigureNetworkInterface failed: %w", err)
	}
	return nil
}

// legacyCall calls a legacy system or network route ({error, msg, output}).
func (capi *CalaosApi) legacyCall(token, method, path string, body, out any) error {
	resp, b, err := capi.callWithToken(method, path, token, nil, body)
	if err != nil {
		return err
	}

	var ar ApiResponse
	if err := json.Unmarshal(b, &ar); err != nil {
		return apiErrorOr(resp, b, err)
	}
	if ar.Error {
		return &APIError{Status: resp.StatusCode, Msg: ar.Msg}
	}
	if out != nil {
		return json.Unmarshal(ar.Output, out)
	}
	return nil
}

func apiErrorOr(resp *http.Response, body []byte, err error) error {
	if aerr := apiError(resp, body); aerr != nil {
		return aerr
	}
	return err
}
