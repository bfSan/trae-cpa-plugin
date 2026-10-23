package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestExchangeSafeDiagnosticsNoReplay(t *testing.T) {
	cases := []struct {
		name                 string
		status               int
		body, category, code string
		transport            bool
	}{
		{"404", 404, "404 page not found auth-code-secret", "http_error", "", false},
		{"500", 500, "<html>token-secret</html>", "http_error", "", false},
		{"business-string", 403, `{"ResponseMetadata":{"Error":{"Code":"20401","Message":"token-secret"}}}`, "business_rejected", "20401", false},
		{"business-number-200", 200, `{"ResponseMetadata":{"Error":{"Code":20405,"Message":"token-secret"}}}`, "business_rejected", "20405", false},
		{"business-with-token", 200, `{"ResponseMetadata":{"Error":{"Code":"10101"}},"Result":{"AccessToken":"token-secret"}}`, "business_rejected", "10101", false},
		{"unknown-code", 400, `{"ResponseMetadata":{"Error":{"Code":"auth-code-secret","Message":"token-secret"}}}`, "business_rejected", "", false},
		{"invalid-json", 200, "<html>token-secret</html>", "invalid_response", "", false},
		{"missing-token", 200, `{"Result":{}}`, "missing_access_token", "", false},
		{"transport", 0, "", "transport_uncertain", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			old := oauthHTTPDo
			defer func() { oauthHTTPDo = old }()
			for _, exchange := range []func([]string, []byte) ([]byte, error){exchangeTokenCandidates, intlexchangeTokenCandidates} {
				hits := 0
				oauthHTTPDo = func(r *http.Request) (*http.Response, error) {
					hits++
					if r.URL.Host != "first.invalid" || r.Method != http.MethodPost {
						t.Error("wrong origin or method")
					}
					if c.transport {
						return nil, errors.New("https://first.invalid/?code=auth-code-secret token-secret")
					}
					response := oauthResponse(c.body)
					response.StatusCode = c.status
					return response, nil
				}
				raw, err := exchange([]string{"https://first.invalid/exchange?code=auth-code-secret", "https://second.invalid/exchange"}, []byte(`{"AuthCode":"auth-code-secret"}`))
				var failure *loginExchangeFailure
				if raw != nil || !errors.As(err, &failure) {
					t.Fatalf("expected typed failure: %v", err)
				}
				if failure.Category != c.category || failure.HTTPStatus != c.status || failure.BizCode != c.code {
					t.Fatalf("unexpected failure %+v", failure)
				}
				if hits != 1 {
					t.Fatalf("one-time code submitted %d times", hits)
				}
				encoded, _ := json.Marshal(failure)
				for _, secret := range []string{"token-secret", "auth-code-secret", "first.invalid", "second.invalid"} {
					if strings.Contains(err.Error()+string(encoded), secret) {
						t.Fatal("unsafe diagnostic")
					}
				}
			}
		})
	}
}

func TestExchangeTokenEnvelopeParity(t *testing.T) {
	for _, envelope := range []string{"Result", "result", "top"} {
		for _, tokenKey := range []string{"AccessToken", "accessToken", "Token", "token"} {
			t.Run(envelope+"/"+tokenKey, func(t *testing.T) {
				fields := map[string]any{tokenKey: "access-secret", "refreshToken": "refresh-secret", "TokenExpireAt": int64(2000000000000)}
				var body any = fields
				if envelope != "top" {
					body = map[string]any{envelope: fields}
				}
				raw, _ := json.Marshal(body)
				a, r, e := parseExchangeTokenResponse(raw)
				if a != "access-secret" || r != "refresh-secret" || e != 2000000000 || extractExchangeAccessToken(raw) != a || intlextractExchangeAccessToken(raw) != a {
					t.Fatal("validation and checkpoint parsing differ")
				}
			})
		}
	}
}

func oauthIntlDiagnosticContext(t *testing.T, state string) *intlloginCtx {
	t.Helper()
	cn := oauthTestContext(t, state)
	loginStates.Delete(state)
	lc := &intlloginCtx{state: state, authDir: cn.authDir, codeVerifier: cn.codeVerifier, deviceID: cn.deviceID, machineID: cn.machineID, devicePublicKey: cn.devicePublicKey, devicePrivateKey: cn.devicePrivateKey, expires: cn.expires, authCode: cn.authCode}
	lc.CreatedAt = cn.CreatedAt
	if err := registerLoginIntl(lc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { intlloginStates.Delete(state) })
	return lc
}

func TestIntlExchangeFailurePersistsAndPublicStatusIsTerminal(t *testing.T) {
	lc := oauthIntlDiagnosticContext(t, "intl-diagnostic-1")
	old := oauthHTTPDo
	defer func() { oauthHTTPDo = old }()
	count := 0
	oauthHTTPDo = func(*http.Request) (*http.Response, error) {
		count++
		r := oauthResponse(`{"ResponseMetadata":{"Error":{"Code":20405,"Message":"secret"}}}`)
		r.StatusCode = 401
		return r, nil
	}
	_, first := advanceLoginIntl(lc, true)
	if first == nil || !strings.Contains(first.Error(), "20405") {
		t.Fatalf("missing safe error: %v", first)
	}
	intlloginStates.Delete(lc.state)
	if restorePendingLoginState(lc.state) != lc.state {
		t.Fatal("restore failed")
	}
	v, _ := intlloginStates.Load(lc.state)
	restored := v.(*intlloginCtx)
	_, second := advanceLoginIntl(restored, true)
	if second == nil || second.Error() != first.Error() || count != 1 {
		t.Fatal("lost diagnostic or resubmitted code")
	}
	status := oauthLoginStatus(lc.state)
	if status.Pending || status.Retryable || !status.RestartRequired || status.ExchangeFailure == nil || status.ExchangeFailure.HTTPStatus != 401 || status.ExchangeFailure.BizCode != "20405" {
		t.Fatalf("wrong terminal status %+v", status)
	}
	public := handleLoginStatusResource()
	var decoded loginStatusPayload
	if json.Unmarshal(public, &decoded) != nil || !decoded.RestartRequired || decoded.Pending || decoded.State != "" || decoded.ExchangeFailure == nil {
		t.Fatalf("wrong public terminal payload: %s", public)
	}
	for _, secret := range []string{lc.state, lc.authCode, lc.codeVerifier, lc.devicePrivateKey, "secret"} {
		if strings.Contains(string(public), secret) {
			t.Fatal("public diagnostic leaked sensitive material")
		}
	}
	request, _ := json.Marshal(map[string]string{"state": lc.state})
	poll := handleOAuthManagementPoll(pluginapi.ManagementRequest{Body: request})
	if !strings.Contains(string(poll.Body), `"restart_required":true`) || !strings.Contains(string(poll.Body), `"biz_code":"20405"`) || count != 1 {
		t.Fatalf("poll lost diagnostics or replayed: %s", poll.Body)
	}
}

func TestExchangeStatusDistinguishesRunningTokenAndLegacyFailure(t *testing.T) {
	lc := oauthIntlDiagnosticContext(t, "intl-stage-diagnostic-1")
	lc.ExchangeStarted = true
	status := oauthLoginStatus(lc.state)
	if !status.Pending || status.RestartRequired || status.ExchangeFailure != nil {
		t.Fatal("in-progress exchange marked failed")
	}
	lc.Failure = "old generic failure secret"
	status = oauthLoginStatus(lc.state)
	if status.Pending || !status.RestartRequired || status.ExchangeFailure.Category != "outcome_uncertain" {
		t.Fatal("legacy terminal failure hidden")
	}
	lc.Token = &loginToken{Access: "access-secret"}
	status = oauthLoginStatus(lc.state)
	if !status.Pending || !status.Retryable || status.RestartRequired || status.ExchangeFailure != nil {
		t.Fatal("token checkpoint incorrectly requires new login")
	}
	lc.Token = nil
	lc.Result = &pluginapi.AuthData{}
	status = oauthLoginStatus(lc.state)
	if !status.Pending || !status.Retryable || status.RestartRequired || status.ExchangeFailure != nil {
		t.Fatal("result checkpoint incorrectly requires new login")
	}
	if err := exchangeOnce(&lc.loginProgress, func() error { return nil }, func() (*loginToken, error) {
		t.Fatal("result checkpoint re-exchanged code")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	var restored loginProgress
	restoreLoginProgress(&restored, &loginProgress{ExchangeStarted: true})
	if restored.ExchangeFailure == nil || restored.ExchangeFailure.Category != "outcome_uncertain" {
		t.Fatal("crashed exchange is not terminal")
	}
}

func TestIntlTopLevelTokenCheckpointSurvivesSaveFailure(t *testing.T) {
	mockOAuthFlow(t)
	lc := oauthIntlDiagnosticContext(t, "intl-top-level-1")
	calls := 0
	oauthHTTPDo = func(r *http.Request) (*http.Response, error) {
		calls++
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil || request["CodeVerifier"] != lc.codeVerifier || request["AuthCode"] != lc.authCode {
			t.Fatal("lost PKCE or auth code")
		}
		info, _ := request["DeviceInfo"].(map[string]any)
		if info["DevicePublicKey"] != lc.devicePublicKey {
			t.Fatal("lost device proof")
		}
		return oauthResponse(`{"accessToken":"access-secret","refreshToken":"refresh-secret","TokenExpireAt":2000000000}`), nil
	}
	intlupstreamClient.HTTP = &http.Client{Transport: oauthTestTransport(func(*http.Request) (*http.Response, error) {
		return oauthResponse(`{"Result":{"UserID":"intl-uid","ScreenName":"mock"}}`), nil
	})}
	oldSave := loginSaveResult
	defer func() { loginSaveResult = oldSave }()
	saves := 0
	loginSaveResult = func(string, *pluginapi.AuthData) error {
		saves++
		if saves == 1 {
			return errors.New("save failed")
		}
		return nil
	}
	if _, err := advanceLoginIntl(lc, true); err == nil {
		t.Fatal("expected save failure")
	}
	if lc.Token == nil || lc.Token.Access != "access-secret" || lc.Token.Refresh != "refresh-secret" || lc.Result == nil {
		t.Fatal("successful top-level exchange lost its token")
	}
	intlloginStates.Delete(lc.state)
	if restorePendingLoginState(lc.state) != lc.state {
		t.Fatal("restore failed")
	}
	v, _ := intlloginStates.Load(lc.state)
	if _, err := advanceLoginIntl(v.(*intlloginCtx), true); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || saves != 2 {
		t.Fatalf("exchange/save counts %d/%d", calls, saves)
	}
}

func TestCallbackReceiptDoesNotClaimSavedLogin(t *testing.T) {
	for _, intl := range []bool{false, true} {
		t.Run(fmt.Sprint(intl), func(t *testing.T) {
			state := "callback-not-saved-" + fmt.Sprint(intl)
			cn := oauthTestContext(t, state)
			// Host ownership disables detached self-completion in a callback-only test.
			cn.Owner = "host"
			if intl {
				loginStates.Delete(state)
				lc := &intlloginCtx{state: state, expires: cn.expires}
				lc.Owner = "host"
				intlloginStates.Store(state, lc)
				defer intlloginStates.Delete(state)
			}
			body := handleOAuthCallbackResource(pluginapi.ManagementRequest{Query: url.Values{"state": {state}, "authCode": {"mock-code"}}})
			if strings.Contains(string(body), "Login successful") || !strings.Contains(string(body), "授权回调已收到") || !strings.Contains(string(body), "not yet confirmed") {
				t.Fatalf("callback overclaims success: %s", body)
			}
		})
	}
}
