package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/netroute"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func mockHTTPBridge(t *testing.T, call func(string, []byte) ([]byte, error)) {
	t.Helper()
	old := routeHostCall
	routeHostCall = call
	t.Cleanup(func() { routeHostCall = old; netroute.InstallBridge(nil); _ = netroute.Configure("") })
	_ = netroute.Configure("")
	netroute.InstallBridge(traeBridgeRoundTrip)
}
func TestTraeBridgeAcceptsHostResponseFieldNames(t *testing.T) {
	for _, field := range []string{"StatusCode", "status_code"} {
		t.Run(field, func(t *testing.T) {
			mockHTTPBridge(t, func(method string, raw []byte) ([]byte, error) {
				if method != pluginabi.MethodHostHTTPDo {
					t.Fatal(method)
				}
				var req traeHTTPWire
				if json.Unmarshal(raw, &req) != nil || req.Request.Method != "POST" || string(req.Request.Body) != "payload" {
					t.Fatal("request not preserved")
				}
				return routeMarshal(map[string]any{"ok": true, "result": map[string]any{field: 201, "Headers": http.Header{"Content-Type": {"application/json"}}, "Body": []byte("reply")}}), nil
			})
			req, _ := http.NewRequest("POST", "https://example.invalid/exchange", strings.NewReader("payload"))
			resp, e := RoutedHTTPDo(req)
			if e != nil {
				t.Fatal(e)
			}
			defer resp.Body.Close()
			b, e := io.ReadAll(resp.Body)
			if e != nil || string(b) != "reply" || resp.StatusCode != 201 {
				t.Fatalf("%q %d %v", b, resp.StatusCode, e)
			}
		})
	}
}
func TestTraeBridgeFailureIsTerminalAndSanitized(t *testing.T) {
	for _, raw := range []string{`not-json`, `{"ok":false,"error":{"message":"token=private"}}`, `{"ok":true,"result":{"StatusCode":0}}`} {
		t.Run(raw, func(t *testing.T) {
			calls := 0
			mockHTTPBridge(t, func(string, []byte) ([]byte, error) { calls++; return []byte(raw), nil })
			req, _ := http.NewRequest("POST", "http://127.0.0.1:1?verifier=private", strings.NewReader("body"))
			_, e := RoutedHTTPDo(req)
			if e == nil || calls != 1 || strings.Contains(e.Error(), "private") {
				t.Fatalf("calls %d safe error %v", calls, e)
			}
		})
	}
}
func TestTraeHostStreamPreservesFinalPayloadAndClosesOnce(t *testing.T) {
	closes, reads := 0, 0
	mockHTTPBridge(t, func(m string, b []byte) ([]byte, error) {
		switch m {
		case pluginabi.MethodHostHTTPDoStream:
			return []byte(`{"ok":true,"result":{"status_code":200,"stream_id":"s"}}`), nil
		case pluginabi.MethodHostHTTPStreamRead:
			reads++
			return routeMarshal(map[string]any{"ok": true, "result": map[string]any{"payload": []byte("chunk"), "done": true, "error": "private upstream url"}}), nil
		case pluginabi.MethodHostHTTPStreamClose:
			closes++
			return []byte(`{"ok":true,"result":{}}`), nil
		}
		return nil, errors.New("unexpected method")
	})
	req, _ := http.NewRequest("GET", "https://example.invalid/events", nil)
	resp, e := RoutedHTTPDo(req)
	if e != nil {
		t.Fatal(e)
	}
	body, e := io.ReadAll(resp.Body)
	if string(body) != "chunk" || e == nil || strings.Contains(e.Error(), "private") {
		t.Fatalf("data %q err %v", body, e)
	}
	resp.Body.Close()
	resp.Body.Close()
	if reads != 1 || closes != 1 {
		t.Fatalf("reads %d closes %d", reads, closes)
	}
}
func TestTraeBridgeContextCancellationPreventsSubmission(t *testing.T) {
	calls := 0
	mockHTTPBridge(t, func(string, []byte) ([]byte, error) { calls++; return nil, errors.New("unexpected") })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://example.invalid", nil)
	_, e := RoutedHTTPDo(req)
	if !errors.Is(e, context.Canceled) || calls != 0 {
		t.Fatalf("calls %d err %v", calls, e)
	}
}
func TestTraeRuntimeProxyConfigRejectsMalformedValueWithoutPanic(t *testing.T) {
	oldMode := netroute.Mode()
	defer func() {
		if oldMode == "inherit" {
			_ = netroute.Configure("")
		}
	}()
	for _, yaml := range []string{"proxy-url: [bad]\n", "proxy-url: http://proxy:99999\n", "proxy-url: true\n", "proxy-url: http://one\nproxy-url: http://two\n"} {
		configureRuntime(routeMarshal(map[string]any{"config_yaml": []byte(yaml)}))
		if netroute.Mode() != "blocked" {
			t.Fatal("bad routing not blocked")
		}
	}
	configureRuntime(routeMarshal(map[string]any{"config_yaml": []byte("checkin_auto: false\ntoken_keepalive: false\n")}))
	if netroute.Mode() != "inherit" {
		t.Fatal("empty proxy did not recover")
	}
}
