package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestPastedCallbackHTMLEntitiesOnce(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"plain URL entity prefix intact", "http://127.0.0.1:9/authorize?loginTraceID=dummy&notebook=unchanged", "http://127.0.0.1:9/authorize?loginTraceID=dummy&notebook=unchanged"},
		{"amp", "http://127.0.0.1:9/authorize?loginTraceID=dummy&amp;authCodeInfo=x", "http://127.0.0.1:9/authorize?loginTraceID=dummy&authCodeInfo=x"},
		{"numeric", "http://127.0.0.1:9/authorize?loginTraceID=dummy&#38;authCodeInfo=x", "http://127.0.0.1:9/authorize?loginTraceID=dummy&authCodeInfo=x"},
		{"one boundary only", "http://127.0.0.1:9/authorize?loginTraceID=dummy&amp;amp;authCodeInfo=x", "http://127.0.0.1:9/authorize?loginTraceID=dummy&amp;authCodeInfo=x"},
		{"encoded grant intact", "http://127.0.0.1:9/authorize?loginTraceID=dummy&amp;authCode=dummy%26amp%3Bvalue", "http://127.0.0.1:9/authorize?loginTraceID=dummy&authCode=dummy%26amp%3Bvalue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, msg := sanitizePastedCallback(tc.raw)
			if msg != "" || got != tc.want {
				t.Fatalf("got %q / %q, want %q", got, msg, tc.want)
			}
		})
	}
}

func TestSubmitInitiationPreservesPending(t *testing.T) {
	const state = "test-initiation-only-dummy"
	cn := &loginCtx{variant: variantCN, state: state, loginTraceID: state, expires: time.Now().Add(time.Minute)}
	intl := &intlloginCtx{state: state, loginTraceID: state, expires: time.Now().Add(time.Minute)}
	loginStates.Store(state, cn)
	defer loginStates.Delete(state)
	intlloginStates.Store(state, intl)
	defer intlloginStates.Delete(state)
	for _, raw := range []string{
		"https://www.trae.ai/authorization?client_id=dummy&login_trace_id=" + state + "&code_challenge=dummy",
		"https://www.trae.cn/authorization?client_id=dummy&amp;login_trace_id=" + state + "&amp;code_challenge=dummy",
		"client_id=dummy&redirect_uri=http%3A%2F%2F127.0.0.1%3A9%2Fauthorize&code_challenge=dummy",
	} {
		body, _ := json.Marshal(map[string]string{"url": raw})
		out := string(handleOAuthSubmitResource(pluginapi.ManagementRequest{Method: "POST", Body: body}))
		if !strings.Contains(out, "Authorization not completed") || !strings.Contains(out, "pending login is unchanged") {
			t.Fatalf("missing initiation guidance: %s", out)
		}
		if cn.authCode != "" || cn.refreshToken != "" || cn.err != nil || cn.ExchangeStarted || cn.done != nil {
			t.Fatal("CN pending mutated")
		}
		if intl.authCode != "" || intl.err != nil || intl.ExchangeStarted || intl.done != nil {
			t.Fatal("Intl pending mutated")
		}
	}
}

func TestInitiationClassificationPreservesCallbackResults(t *testing.T) {
	for _, key := range []string{"authCodeInfo", "auth_code_info", "AuthCodeInfo", "authCode", "auth_code", "AuthCode", "authorization_code", "code", "refreshToken", "refresh_token", "RefreshToken", "refresh-token", "error", "error_code", "err", "errorCode", "isRedirect"} {
		u := &url.URL{Path: "/authorization"}
		q := url.Values{"client_id": {"dummy"}, "code_challenge": {"dummy"}, key: {"dummy-result"}}
		if isAuthorizationInitiationURL(u, q) {
			t.Errorf("callback result %s misclassified", key)
		}
	}
}

func TestIntlVerificationURIRawQueryContract(t *testing.T) {
	verifier, challenge := generatePKCEPair()
	sum := sha256.Sum256([]byte(verifier))
	if challenge != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatal("PKCE is not S256 base64url")
	}
	raw := intlbuildVerificationURI("www.trae.ai", intlverificationURIParams{AuthFrom: "trae", ClientID: "dummy-client", LoginTraceID: "dummy-state", CallbackURL: "http://127.0.0.1:9/authorize", DeviceID: "dummy-device", MachineID: "dummy-machine", CodeChallenge: challenge})
	if strings.Contains(raw, "&amp;") {
		t.Fatal("Go generated HTML-escaped authorization URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "www.trae.ai" || u.Path != "/authorization" {
		t.Fatal("Intl host or route changed")
	}
	// Match auth.login.start -> management response -> browser JSON parsing,
	// without starting a real login or allocating/persisting any pending flow.
	wire, err := okEnvelope(pluginapi.AuthLoginStartResponse{URL: raw, State: "dummy-state"})
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Result pluginapi.AuthLoginStartResponse
	}
	if err := json.Unmarshal(wire, &env); err != nil {
		t.Fatal(err)
	}
	response := mgmtJSONResponse(200, map[string]any{"url": env.Result.URL})
	var browser struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(response.Body, &browser); err != nil || browser.URL != raw {
		t.Fatal("management JSON wrapper changed the authorization URL")
	}
	q := u.Query()
	for k, want := range map[string]string{"auth_from": "trae", "auth_callback_url": "http://127.0.0.1:9/authorize", "code_challenge": challenge, "code_challenge_method": "S256", "login_trace_id": "dummy-state", "device_id": "dummy-device", "x_device_id": "dummy-device", "machine_id": "dummy-machine", "x_machine_id": "dummy-machine"} {
		if q.Get(k) != want {
			t.Errorf("%s mismatch", k)
		}
	}
	if q.Has("redirect_uri") || q.Has("code_verifier") || q.Has("DeviceProof") {
		t.Fatal("unexpected callback or exchange-only query field")
	}
}
