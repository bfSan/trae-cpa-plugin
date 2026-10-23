// oauth_fallback_shortcircuit_test.go — v0.12.76: the login ExchangeToken
// fan-out must SHORT-CIRCUIT on a business rejection (a 4xx carrying a
// Volcengine ResponseMetadata.Error.Code such as 20401 Device limit reached
// or 10101) instead of fanning the same doomed request out to every
// remaining origin — the user-reported noise chain was api.trae.cn 403+20401
// → api.trae.com.cn 400+10101 → www.trae.cn WAF HTML. Network errors, 404s,
// 5xx and HTML bodies are also non-replayable (v0.13.0 onward).
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestExchangeShortCircuitsOnBusinessRejection: a 403 with the 20401
// envelope must abort the fan-out immediately (server B is never hit) and
// the error must carry the code, the upstream message and the bilingual
// recovery hint.
func TestExchangeShortCircuitsOnBusinessRejection(t *testing.T) {
	var bHits int32
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"ResponseMetadata":{"Error":{"Code":"20401","Message":"Device limit reached."}}}`))
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&bHits, 1)
		_, _ = w.Write([]byte(`{"Result":{"AccessToken":"tok"}}`))
	}))
	defer b.Close()

	_, err := exchangeTokenCandidates([]string{a.URL + "/x", b.URL + "/x"}, []byte(`{}`))
	if err == nil {
		t.Fatal("expected the 20401 rejection to fail the exchange")
	}
	for _, want := range []string{"20401", "Device limit", "设备数已达上限"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
	if got := atomic.LoadInt32(&bHits); got != 0 {
		t.Fatalf("fan-out continued after a business rejection: server B hit %d times", got)
	}
}

// TestExchangeDoesNotReplayOn404AndHTML: a 404 with an HTML body is a transport
// concern, not proof that a code was unconsumed — do not call the next
// candidate.
func TestExchangeDoesNotReplayOn404AndHTML(t *testing.T) {
	var aHits, bHits int32
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&aHits, 1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<!doctype html><html>404 page not found</html>"))
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&bHits, 1)
		_, _ = w.Write([]byte(`{"Result":{"AccessToken":"tok"}}`))
	}))
	defer b.Close()

	raw, err := exchangeTokenCandidates([]string{a.URL + "/x", b.URL + "/x"}, []byte(`{}`))
	if err == nil || raw != nil {
		t.Fatal("ambiguous exchange must fail without retry")
	}
	if atomic.LoadInt32(&aHits) != 1 || atomic.LoadInt32(&bHits) != 0 {
		t.Fatalf("unexpected fanout a=%d b=%d", aHits, bHits)
	}
}

// TestExchangeDoesNotReplayOn5xx: a 500 without the ResponseMetadata.Error.Code
// envelope still cannot prove non-consumption — do not fall back.
func TestExchangeDoesNotReplayOn5xx(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Result":{"AccessToken":"tok"}}`))
	}))
	defer b.Close()

	raw, err := exchangeTokenCandidates([]string{a.URL + "/x", b.URL + "/x"}, []byte(`{}`))
	if err == nil || raw != nil {
		t.Fatal("5xx exchange must not retry code")
	}
}

// TestExchange10101AlsoShortCircuits: any business-code 4xx short-circuits,
// not just 20401 — the reported chain also burned a 400 code=10101 on
// api.trae.com.cn after the account was already rejected.
func TestExchange10101AlsoShortCircuits(t *testing.T) {
	var bHits int32
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ResponseMetadata":{"Error":{"Code":"10101","Message":"无效参数"}}}`))
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&bHits, 1)
		_, _ = w.Write([]byte(`{"Result":{"AccessToken":"tok"}}`))
	}))
	defer b.Close()

	_, err := exchangeTokenCandidates([]string{a.URL + "/x", b.URL + "/x"}, []byte(`{}`))
	if err == nil {
		t.Fatal("expected the 10101 rejection to fail the exchange")
	}
	if !strings.Contains(err.Error(), "10101") {
		t.Fatalf("error missing the upstream code: %v", err)
	}
	if got := atomic.LoadInt32(&bHits); got != 0 {
		t.Fatalf("fan-out continued after a business rejection: server B hit %d times", got)
	}
}

// TestIntlExchangeShortCircuitsOnBusinessRejection: the intl twin
// (intlexchangeTokenCandidates) shares the short-circuit — parity guard for
// the same edit in intl_oauth_fallback.go.
func TestIntlExchangeShortCircuitsOnBusinessRejection(t *testing.T) {
	var bHits int32
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"ResponseMetadata":{"Error":{"Code":"20401","Message":"Device limit reached."}}}`))
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&bHits, 1)
		_, _ = w.Write([]byte(`{"Result":{"AccessToken":"tok"}}`))
	}))
	defer b.Close()

	_, err := intlexchangeTokenCandidates([]string{a.URL + "/x", b.URL + "/x"}, []byte(`{}`))
	if err == nil {
		t.Fatal("expected the 20401 rejection to fail the intl exchange")
	}
	if !strings.Contains(err.Error(), "20401") || !strings.Contains(err.Error(), "设备数已达上限") {
		t.Fatalf("error missing code/hint: %v", err)
	}
	if got := atomic.LoadInt32(&bHits); got != 0 {
		t.Fatalf("intl fan-out continued after a business rejection: server B hit %d times", got)
	}
}
