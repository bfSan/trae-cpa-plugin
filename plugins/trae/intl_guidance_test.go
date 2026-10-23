package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestIntlLoginGuidance_FallsBackToDefaultHost pins the v0.12.10 degrade
// behavior: when every Intl GetLoginGuidance endpoint is unreachable, the
// helper returns the default Intl login host instead of erroring out — the
// browser-facing auth-url request must never fail (or block for 45s) just
// because guidance probing failed. This mirrors the CN flow, which has
// degraded to its default host since v0.12.9 (the "网络错误" fix).
func TestIntlLoginGuidance_FallsBackToDefaultHost(t *testing.T) {
	origTimeoutGuard := intlLoginGuidanceProbeTimeout
	intlLoginGuidanceProbeTimeout = 200 * time.Millisecond
	defer func() { intlLoginGuidanceProbeTimeout = origTimeoutGuard }()

	origURLs := intlLoginGuidanceURLs
	// An unroutable local port: connection refused inside the probe timeout.
	intlLoginGuidanceURLs = []string{
		"http://127.0.0.1:1/cloudide/api/v3/trae/GetLoginGuidance",
	}
	defer func() { intlLoginGuidanceURLs = origURLs }()

	host, err := intlrequestLoginGuidance(false, "trace-fallback-test")
	if err != nil {
		t.Fatalf("expected default-host fallback, got error: %v", err)
	}
	if host != "https://www.trae.ai" {
		t.Errorf("fallback must be the Intl website, got %q", host)
	}
	verificationURL, err := url.Parse(intlbuildVerificationURI(host, intlverificationURIParams{}))
	if err != nil || verificationURL.Host != "www.trae.ai" || verificationURL.Path != "/authorization" {
		t.Fatal("fallback did not produce the public Intl authorization route")
	}
	if intlOAuthDefaultHost != "https://api.marscode.com" {
		t.Fatal("account API origin must not be changed with the browser fallback")
	}
}

// TestIntlLoginGuidance_ParsesLoginHost verifies the happy path: a 2xx JSON
// carrying Result.LoginHost is extracted verbatim.
func TestIntlLoginGuidance_ParsesLoginHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" || !strings.Contains(r.Header.Get("User-Agent"), "antigravity-cockpit-tools") {
			t.Errorf("User-Agent missing cockpit-tools marker: %q", r.Header.Get("User-Agent"))
		}
		_, _ = w.Write([]byte(`{"Result":{"LoginHost":"www.trae.ai"}}`))
	}))
	defer srv.Close()

	origURLs := intlLoginGuidanceURLs
	u, _ := url.Parse(srv.URL)
	intlLoginGuidanceURLs = []string{srv.URL + "/cloudide/api/v3/trae/GetLoginGuidance"}
	defer func() { intlLoginGuidanceURLs = origURLs; _ = u }()

	host, err := intlrequestLoginGuidance(false, "trace-parse-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if host != "www.trae.ai" {
		t.Errorf("host = %q, want www.trae.ai", host)
	}
}
