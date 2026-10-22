// Package netroute owns Trae's single outbound routing boundary. It never
// chooses accounts and never replays a failed host request on another route.
package netroute

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type Bridge func(*http.Request, bool) (*http.Response, error)
type bridgeState struct{ call Bridge }
type routeState struct {
	mode      string
	transport http.RoundTripper
}

var bridge atomic.Pointer[bridgeState]
var route atomic.Pointer[routeState]
var inherited = transport(nil)

func transport(proxy *url.URL) *http.Transport {
	p := http.ProxyFromEnvironment
	if proxy != nil {
		p = http.ProxyURL(proxy)
	}
	return &http.Transport{Proxy: p, DialContext: (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext, MaxIdleConns: 100, MaxIdleConnsPerHost: 20, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 120 * time.Second}
}

// InstallBridge is called once at host initialization. A nil bridge means
// standalone compatibility mode, never fallback after an attempted bridge call.
func InstallBridge(call Bridge) {
	if call == nil {
		bridge.Store(nil)
	} else {
		bridge.Store(&bridgeState{call: call})
	}
}
func Block() { route.Store(&routeState{mode: "blocked"}) }
func Configure(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		route.Store(&routeState{mode: "inherit"})
		return nil
	}
	invalid := func() error {
		Block()
		return errors.New("invalid Trae proxy configuration; outbound requests blocked")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return invalid()
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return invalid()
	}
	if u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return invalid()
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return invalid()
		}
	}
	if strings.HasSuffix(u.Host, ":") {
		return invalid()
	}
	u.Scheme = strings.ToLower(u.Scheme)
	route.Store(&routeState{mode: "explicit", transport: transport(u)})
	return nil
}
func Mode() string {
	if s := route.Load(); s != nil {
		return s.mode
	}
	return "inherit"
}

// safeError deliberately excludes URL, response body and proxy credentials.
// It preserves only cancellation/timeout classification, not a secret-bearing
// underlying url.Error that a caller could accidentally print after Unwrap.
type safeError struct{ timeout bool }

func (e *safeError) Error() string   { return "Trae outbound HTTP request failed" }
func (e *safeError) Timeout() bool   { return e.timeout }
func (e *safeError) Temporary() bool { return e.timeout }
func SafeError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var ne net.Error
	return &safeError{timeout: errors.As(err, &ne) && ne.Timeout()}
}

type routedTransport struct{ stream bool }

func (t routedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("missing outbound request")
	}
	if req.Context().Err() != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, req.Context().Err()
	}
	r := route.Load()
	if r != nil && r.mode == "blocked" {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, errors.New("Trae proxy configuration is invalid; outbound requests blocked")
	}
	var resp *http.Response
	var err error
	if r != nil && r.mode == "explicit" {
		resp, err = r.transport.RoundTrip(req)
	} else if b := bridge.Load(); b != nil && b.call != nil {
		resp, err = b.call(req, t.stream)
	} else {
		resp, err = inherited.RoundTrip(req)
	}
	if err != nil {
		return nil, SafeError(req.Context(), err)
	}
	if resp == nil || resp.StatusCode < 100 || resp.StatusCode > 599 {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, errors.New("invalid Trae outbound HTTP response")
	}
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	resp.Body = &safeBody{ReadCloser: resp.Body, ctx: req.Context()}
	return resp, nil
}

type safeBody struct {
	io.ReadCloser
	ctx context.Context
}

func (b *safeBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	if e != nil && e != io.EOF && e != io.ErrUnexpectedEOF {
		e = SafeError(b.ctx, e)
	}
	return n, e
}
func (b *safeBody) Close() error { return SafeError(b.ctx, b.ReadCloser.Close()) }

// Do removes net/http's outer url.Error, which otherwise reintroduces a
// sensitive request URL around an already sanitized transport failure.
func Do(client *http.Client, req *http.Request) (*http.Response, error) {
	if client == nil || req == nil {
		return nil, errors.New("missing outbound HTTP client or request")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, SafeError(req.Context(), err)
	}
	return resp, nil
}
func NewClient(timeout time.Duration, stream bool) *http.Client {
	return &http.Client{Timeout: timeout, Transport: routedTransport{stream: stream}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
