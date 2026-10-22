package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/netroute"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"gopkg.in/yaml.v3"
)

var routedOAuthClient = netroute.NewClient(30*time.Second, false)

func RoutedHTTPDo(req *http.Request) (*http.Response, error) {
	return netroute.Do(routedOAuthClient, req)
}

var routeHostCall = hostCall

func initTraeHTTPBridge() {
	if hostAPI != nil && hostAPI.call != nil {
		netroute.InstallBridge(traeBridgeRoundTrip)
	}
}

// configureRuntime fails closed only for the outbound proxy, not for the
// entire provider. The host persists configuration before reconfigure.
func configureRuntime(raw []byte) {
	var cfg struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		netroute.Block()
		return
	}
	var node yaml.Node
	if len(cfg.ConfigYAML) > 0 && yaml.Unmarshal(cfg.ConfigYAML, &node) != nil {
		netroute.Block()
		return
	}
	proxy := ""
	valid := true
	auto, keepalive := true, true
	seen := map[string]bool{}
	if len(node.Content) > 0 {
		root := node.Content[0]
		if root.Kind != yaml.MappingNode {
			valid = false
		} else {
			for i := 0; i+1 < len(root.Content); i += 2 {
				k, v := root.Content[i].Value, root.Content[i+1]
				if seen[k] {
					valid = false
				}
				seen[k] = true
				switch k {
				case "proxy-url":
					if v.Kind != yaml.ScalarNode || (v.Tag != "!!str" && v.Tag != "!!null") {
						valid = false
					} else if v.Tag != "!!null" {
						proxy = v.Value
					}
				case "checkin_auto":
					if v.Kind == yaml.ScalarNode && v.Tag == "!!bool" {
						auto = v.Value == "true"
					}
				case "token_keepalive":
					if v.Kind == yaml.ScalarNode && v.Tag == "!!bool" {
						keepalive = v.Value == "true"
					}
				}
			}
		}
	}
	if valid {
		_ = netroute.Configure(proxy)
	} else {
		netroute.Block()
	}
	configureMaintenance(auto, keepalive)
	_ = configureModelOrder(cfg.ConfigYAML)
}

type traeHTTPWire struct {
	Request *traeHTTPInner `json:"request"`
}
type traeHTTPInner struct {
	Method  string              `json:"method"`
	URL     string              `json:"url"`
	Headers map[string][]string `json:"headers"`
	Body    []byte              `json:"body,omitempty"`
}

func routeMarshal(v any) []byte { b, _ := json.Marshal(v); return b }
func routeUnwrap(raw []byte) (json.RawMessage, error) {
	var e struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &e) != nil || !e.OK || len(e.Result) == 0 {
		return nil, errors.New("host HTTP bridge failed")
	}
	return e.Result, nil
}
func traeBridgeRoundTrip(req *http.Request, forceStream bool) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		defer req.Body.Close()
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, netroute.SafeError(req.Context(), err)
		}
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	stream := forceStream || strings.Contains(req.Header.Get("Accept"), "text/event-stream") || strings.HasSuffix(req.URL.Path, "/events")
	method := pluginabi.MethodHostHTTPDo
	if stream {
		method = pluginabi.MethodHostHTTPDoStream
	}
	raw, err := routeHostCall(method, routeMarshal(traeHTTPWire{Request: &traeHTTPInner{Method: req.Method, URL: req.URL.String(), Headers: req.Header, Body: body}}))
	if err != nil {
		return nil, netroute.SafeError(req.Context(), err)
	}
	result, err := routeUnwrap(raw)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		StatusCode int         `json:"StatusCode"`
		StatusAlt  int         `json:"status_code"`
		Headers    http.Header `json:"headers"`
		Body       []byte      `json:"body"`
		StreamID   string      `json:"stream_id"`
		Chunks     []struct {
			Payload []byte `json:"payload"`
			Error   string `json:"error"`
			Done    bool   `json:"done"`
		} `json:"chunks"`
	}
	if json.Unmarshal(result, &decoded) != nil {
		return nil, errors.New("invalid host HTTP bridge response")
	}
	status := decoded.StatusCode
	if status == 0 {
		status = decoded.StatusAlt
	}
	if status < 100 || status > 599 {
		if decoded.StreamID != "" {
			_, _ = routeHostCall(pluginabi.MethodHostHTTPStreamClose, routeMarshal(map[string]string{"stream_id": decoded.StreamID}))
		}
		return nil, errors.New("invalid host HTTP bridge status")
	}
	var reader io.ReadCloser = io.NopCloser(bytes.NewReader(decoded.Body))
	if stream {
		s := &traeBridgeBody{id: decoded.StreamID, ctx: req.Context()}
		for _, c := range decoded.Chunks {
			s.pending = append(s.pending, c.Payload...)
			s.done = s.done || c.Done
			if c.Error != "" {
				s.err = errors.New("upstream stream read failed")
				s.done = true
			}
		}
		if s.id == "" {
			s.done = true
		}
		reader = s
	}
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: decoded.Headers, Body: reader, ContentLength: -1, Request: req}, nil
}

type traeBridgeBody struct {
	id        string
	ctx       context.Context
	pending   []byte
	done      bool
	err       error
	readMu    sync.Mutex
	closeOnce sync.Once
	closed    atomic.Bool
}

func (b *traeBridgeBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	b.readMu.Lock()
	defer b.readMu.Unlock()
	for empty := 0; ; empty++ {
		if b.ctx != nil && b.ctx.Err() != nil {
			_ = b.Close()
			return 0, b.ctx.Err()
		}
		if b.closed.Load() {
			return 0, io.ErrClosedPipe
		}
		if len(b.pending) > 0 {
			n := copy(p, b.pending)
			b.pending = b.pending[n:]
			return n, nil
		}
		if b.done {
			if b.err != nil {
				return 0, b.err
			}
			return 0, io.EOF
		}
		raw, e := routeHostCall(pluginabi.MethodHostHTTPStreamRead, routeMarshal(map[string]string{"stream_id": b.id}))
		if e != nil {
			b.done = true
			b.err = netroute.SafeError(b.ctx, e)
			return 0, b.err
		}
		result, e := routeUnwrap(raw)
		if e != nil {
			b.done = true
			b.err = e
			return 0, e
		}
		var chunk struct {
			Payload []byte `json:"payload"`
			Done    bool   `json:"done"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(result, &chunk) != nil {
			b.done = true
			b.err = errors.New("invalid host stream chunk")
			return 0, b.err
		}
		b.pending = chunk.Payload
		b.done = chunk.Done
		if chunk.Error != "" {
			b.err = errors.New("upstream stream read failed")
			b.done = true
		}
		if empty >= 100 && len(b.pending) == 0 && !b.done {
			b.done = true
			b.err = io.ErrNoProgress
			return 0, b.err
		}
	}
}
func (b *traeBridgeBody) Close() error {
	b.closeOnce.Do(func() {
		b.closed.Store(true)
		if b.id != "" {
			_, _ = routeHostCall(pluginabi.MethodHostHTTPStreamClose, routeMarshal(map[string]string{"stream_id": b.id}))
		}
	})
	return nil
}
