package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// The public authorization page sends the URI's DeviceID to GetPCAuthCode.
// Model that binding at the mocked exchange boundary, including the userInfo
// callback that used to silently substitute an unrelated UID-derived device.
func TestIntlAuthCodeDeviceBindingSurvivesCallbackAndRestart(t *testing.T) {
	for _, tc := range []struct{ name, tag, host string }{
		{"row", "row", "growsg-normal.trae.ai"},
		{"usttp", "USTTP", "grow-normal.traeapi.us"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mockOAuthFlow(t)
			lc := oauthIntlDiagnosticContext(t, "intl-device-binding-"+tc.name)
			lc.authCode = ""
			id := mintDeviceIdentity(variantIntl)
			lc.deviceID, lc.machineID = id.DeviceID, id.MachineID
			lc.devicePublicKey, lc.devicePrivateKey = id.DevicePublicKey, id.DevicePrivateKey
			lc.codeVerifier, lc.codeChallenge = generatePKCEPair()
			uri := intlbuildVerificationURI("www.trae.ai", intlverificationURIParams{
				ClientID: intlupstreamClient.ClientID, DeviceID: lc.deviceID,
				MachineID: lc.machineID, CodeChallenge: lc.codeChallenge,
				CallbackURL: "http://127.0.0.1:9/authorize", LoginTraceID: lc.state,
			})
			u, err := url.Parse(uri)
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			callback := url.Values{
				"loginTraceID": {lc.state}, "host": {"api-sg-central.trae.ai"},
				"userRegion": {"us"}, "userTag": {tc.tag},
				"authCodeInfo": {`{"AuthCode":"mock-one-time-code"}`},
				"userInfo":     {`{"UserID":"mock-account-` + tc.name + `","ScreenName":"mock"}`},
			}
			if msg, accepted := acceptLoginCallbackIntl(lc, callback); msg != "" || !accepted {
				t.Fatalf("callback failed: %s", msg)
			}
			if lc.cbUID != "mock-account-"+tc.name {
				t.Fatal("fixture did not exercise callback identity parsing")
			}
			if lc.deviceID != id.DeviceID || lc.machineID != id.MachineID || lc.devicePublicKey != id.DevicePublicKey || lc.devicePrivateKey != id.DevicePrivateKey {
				t.Fatal("callback replaced the authorization device")
			}
			intlloginStates.Delete(lc.state)
			if restorePendingLoginState(lc.state) != lc.state {
				t.Fatal("could not restore callback checkpoint")
			}
			v, _ := intlloginStates.Load(lc.state)
			lc = v.(*intlloginCtx)
			if lc.devicePrivateKey != id.DevicePrivateKey {
				t.Fatal("restart changed the device private key")
			}
			calls := 0
			oauthHTTPDo = func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodPost || r.URL.Host != tc.host || r.URL.Path != "/trae/api/v3/oauth/ExchangeToken" {
					t.Fatal("wrong exchange method/origin/path")
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Fatal("invalid exchange request")
				}
				if len(body) != 5 || body["ClientID"] != q.Get("client_id") || body["CodeVerifier"] != lc.codeVerifier || body["AuthCode"] != "mock-one-time-code" || body["IDEVersion"] != loadedAppVersion() {
					t.Fatal("auth-code request differs from cockpit protocol")
				}
				sum := sha256.Sum256([]byte(lc.codeVerifier))
				if base64.RawURLEncoding.EncodeToString(sum[:]) != q.Get("code_challenge") || q.Get("code_challenge_method") != "S256" {
					t.Fatal("PKCE changed after callback/restart")
				}
				info, _ := body["DeviceInfo"].(map[string]any)
				if info["DeviceID"] != q.Get("device_id") || info["MachineID"] != q.Get("machine_id") || info["DevicePublicKey"] != id.DevicePublicKey || info["PlatformCode"] != "IDE_PC" {
					t.Fatal("exchange does not match the grant's device binding")
				}
				if _, exists := body["DeviceProof"]; exists || body["req_source"] != nil || body["ClientSecret"] != nil {
					t.Fatal("refresh/checkin-only fields added to auth-code request")
				}
				if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" || r.Header.Get("ClientID") != "" || r.Header.Get("req_source") != "" || r.Header.Get("x-cloudide-token") != "" {
					t.Fatal("auth-code headers differ from cockpit protocol")
				}
				return oauthResponse(`{"Result":{"AccessToken":"mock-access","RefreshToken":"mock-refresh","TokenExpireAt":2000000000}}`), nil
			}
			intlupstreamClient.HTTP = &http.Client{Transport: oauthTestTransport(func(*http.Request) (*http.Response, error) {
				return oauthResponse(`{"Result":{"UserID":"mock-account-` + tc.name + `","ScreenName":"mock"}}`), nil
			})}
			oldSave := loginSaveResult
			t.Cleanup(func() { loginSaveResult = oldSave })
			loginSaveResult = func(_ string, result *pluginapi.AuthData) error {
				var saved struct{ Auth map[string]any }
				if json.Unmarshal(result.StorageJSON, &saved) != nil || saved.Auth["deviceId"] != id.DeviceID || saved.Auth["machineId"] != id.MachineID || saved.Auth["devicePublicKey"] != id.DevicePublicKey || saved.Auth["devicePrivateKey"] != id.DevicePrivateKey {
					t.Fatal("saved credential changed the bound device")
				}
				return nil
			}
			if _, err := advanceLoginIntl(lc, true); err != nil {
				t.Fatal(err)
			}
			if _, err := advanceLoginIntl(lc, true); err != nil || calls != 1 {
				t.Fatal("repeat completion replayed the one-time code")
			}
		})
	}
}

func TestIntlNewAttemptDevicesAreIndependent(t *testing.T) {
	// Same primitive used by intlhandleStartLogin: no shared-install identity
	// read or write and no account is known before the authorization URL.
	first := mintDeviceIdentity(variantIntl)
	second := mintDeviceIdentity(variantIntl)
	if first.DeviceID == "" || first.MachineID == "" || first.DevicePublicKey == "" || first.DevicePrivateKey == "" {
		t.Fatal("incomplete authorization identity")
	}
	if first.DeviceID == second.DeviceID || first.MachineID == second.MachineID || first.DevicePublicKey == second.DevicePublicKey || first.DevicePrivateKey == second.DevicePrivateKey {
		t.Fatal("independent Intl attempts share a device identity")
	}
}
