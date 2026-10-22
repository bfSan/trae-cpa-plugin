package netroute

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func isolate(t *testing.T) {
	t.Helper()
	r, b := route.Load(), bridge.Load()
	t.Cleanup(func() { route.Store(r); bridge.Store(b) })
	InstallBridge(nil)
	_ = Configure("")
}
func TestBridgeFailureNeverReplaysDirect(t *testing.T) {
	isolate(t)
	hits := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(200) }))
	defer s.Close()
	InstallBridge(func(*http.Request, bool) (*http.Response, error) {
		return nil, errors.New("https://proxy:private@host?token=secret")
	})
	for _, stream := range []bool{false, true} {
		r, _ := http.NewRequest("POST", s.URL, strings.NewReader("body"))
		_, e := NewClient(time.Second, stream).Do(r)
		if e == nil {
			t.Fatal("bridge error hidden")
		}
		if strings.Contains(e.Error(), "secret") || strings.Contains(e.Error(), "private") {
			t.Fatal("bridge secret leaked")
		}
	}
	if hits != 0 {
		t.Fatal("failed bridge replayed directly")
	}
}
func TestInvalidProxyBlocksBridgeAndDirect(t *testing.T) {
	isolate(t)
	hits := 0
	InstallBridge(func(*http.Request, bool) (*http.Response, error) { hits++; return nil, errors.New("not expected") })
	for _, url := range []string{"http://user:secret@", "file:///tmp/proxy", "http://proxy:99999", "http://proxy?token=secret", "http://proxy/path", "http://proxy:"} {
		if Configure(url) == nil {
			t.Fatalf("accepted %q", url)
		}
		r, _ := http.NewRequest("GET", "http://127.0.0.1:1", nil)
		_, e := NewClient(time.Second, false).Do(r)
		if e == nil || strings.Contains(e.Error(), "secret") {
			t.Fatal("invalid proxy not safely blocked")
		}
	}
	if hits != 0 {
		t.Fatal("invalid proxy reached bridge")
	}
}
func TestExplicitProxyIsExclusiveAndDoesNotFollowRedirects(t *testing.T) {
	isolate(t)
	hits := 0
	var method, target string
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		method = r.Method
		target = r.URL.String()
		w.Header().Set("Location", "http://should-not-follow.invalid/path")
		w.WriteHeader(307)
	}))
	defer p.Close()
	InstallBridge(func(*http.Request, bool) (*http.Response, error) {
		t.Error("explicit route entered host")
		return nil, errors.New("not expected")
	})
	if e := Configure(p.URL); e != nil {
		t.Fatal(e)
	}
	req, _ := http.NewRequest("POST", "http://upstream.invalid/claim", strings.NewReader("body"))
	resp, e := NewClient(time.Second, false).Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 307 || hits != 1 || method != "POST" || target != "http://upstream.invalid/claim" {
		t.Fatalf("response=%d hits=%d method=%s target=%s", resp.StatusCode, hits, method, target)
	}
}
func TestExplicitProxyFailureDoesNotEnterBridge(t *testing.T) {
	isolate(t)
	calls := 0
	InstallBridge(func(*http.Request, bool) (*http.Response, error) { calls++; return nil, errors.New("unexpected") })
	p := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := p.URL
	p.Close()
	if e := Configure(url); e != nil {
		t.Fatal(e)
	}
	r, _ := http.NewRequest("GET", "http://upstream.invalid", nil)
	_, e := NewClient(time.Second, false).Do(r)
	if e == nil || calls != 0 {
		t.Fatal("explicit proxy failure rerouted")
	}
}
func TestDirectStreamRemainsStreamingAndCancellable(t *testing.T) {
	isolate(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", s.URL, nil)
	resp, e := NewClient(0, true).Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	b := make([]byte, 5)
	if _, e = io.ReadFull(resp.Body, b); e != nil || string(b) != "hello" {
		t.Fatalf("first chunk=%q error=%v", b, e)
	}
	cancel()
	_, e = resp.Body.Read(b)
	if !errors.Is(e, context.Canceled) {
		t.Fatalf("cancel lost: %v", e)
	}
}
func TestInvalidRouteReconfigurationCanRecover(t *testing.T) {
	isolate(t)
	_ = Configure("bad://proxy")
	if Mode() != "blocked" {
		t.Fatal("not blocked")
	}
	if Configure("") != nil || Mode() != "inherit" {
		t.Fatal("valid reset did not recover")
	}
}
