package api

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/calaos/calaos-container/models/structs"
)

const (
	CalaosCtHost = "localhost:8000"
)

type apiOptions struct {
	timeout   time.Duration
	insecure  bool
	proxy     func(*http.Request) (*url.URL, error)
	transport func(*http.Transport)
}

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
		proxy: http.ProxyFromEnvironment,
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

// UpdateCheck forces an update check
func (capi *CalaosApi) UpdateCheck(token string) (imgs *structs.ImageMap, err error) {

	_, body, err := capi.callWithToken("GET", "/api/update/check", token, nil, nil)
	if err != nil {
		return
	}

	imgs = &structs.ImageMap{}
	if err = json.Unmarshal(body, imgs); err != nil {
		return nil, fmt.Errorf("UpdateCheck failed: %v", err)
	}

	return
}

// UpdateAvailable returns available updates
func (capi *CalaosApi) UpdateAvailable(token string) (imgs *structs.ImageMap, err error) {

	_, body, err := capi.callWithToken("GET", "/api/update/available", token, nil, nil)
	if err != nil {
		return
	}

	imgs = &structs.ImageMap{}
	if err = json.Unmarshal(body, imgs); err != nil {
		return nil, fmt.Errorf("UpdateAvailable failed: %v", err)
	}

	return
}

// UpdateImages returns currently installed images
func (capi *CalaosApi) UpdateImages(token string) (imgs *structs.ImageMap, err error) {

	_, body, err := capi.callWithToken("GET", "/api/update/images", token, nil, nil)
	if err != nil {
		return
	}

	imgs = &structs.ImageMap{}
	if err = json.Unmarshal(body, imgs); err != nil {
		return nil, fmt.Errorf("UpdateImages failed: %v", err)
	}

	return
}

// UpgradePackages upgrades all packages
func (capi *CalaosApi) UpgradePackages(token string) (err error) {

	_, _, err = capi.callWithToken("POST", "/api/update/upgrade-all", token, nil, nil)
	if err != nil {
		return
	}

	return
}

// UpdatePackage upgrades a single package
func (capi *CalaosApi) UpdatePackage(token string, pkg string) (err error) {

	params := url.Values{
		"package": []string{pkg},
	}

	_, _, err = capi.callWithToken("POST", "/api/update/upgrade", token, params, nil)
	if err != nil {
		return
	}

	return
}

// UpgradeStatus returns the status of the upgrade
func (capi *CalaosApi) UpgradeStatus(token string) (status *structs.Status, err error) {

	_, body, err := capi.callWithToken("GET", "/api/update/status", token, nil, nil)
	if err != nil {
		return
	}

	status = &structs.Status{}
	if err = json.Unmarshal(body, status); err != nil {
		return nil, fmt.Errorf("UpgradeStatus failed: %v", err)
	}

	return
}
