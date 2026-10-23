// Package main implements the trae-intl CLIProxyAPI dynamic plugin.
//
// trae-intl wraps Trae Intl (api.marscode.com + core-normal.trae.ai) as a
// cliproxy provider: Trae OAuth (GetLoginGuidance + AuthCode ExchangeToken),
// Cloud-IDE-JWT auth, Web SOLO remote chat_sessions + events SSE protocol,
// v1 credit API. Intl has no daily check-in.
//
// Protocol layer based on OmniRoute/open-sse/executors/trae.ts (MIT, by
// diegosouzapw). Translated from TypeScript to Go.
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
        void* ptr;
        size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
        uint32_t abi_version;
        void* host_ctx;
        cliproxy_host_call_fn call;
        cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
        uint32_t abi_version;
        cliproxy_plugin_call_fn call;
        cliproxy_plugin_free_fn free_buffer;
        cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
        stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
        if (stored_host == NULL || stored_host->call == NULL) {
                return 1;
        }
        return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
        if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
                stored_host->free_buffer(ptr, len);
        }
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/intlupstream"
	cnupstream "github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	intlproviderName = "trae"
	intlauthFileName = "trae-intl.json"
	// OAuth constants (Intl uses api.marscode.com, not api.trae.cn)
	intlOAuthLoginGuidanceURL = "https://api.marscode.com/cloudide/api/v3/trae/GetLoginGuidance"
	intlOAuthDefaultHost      = "https://api.marscode.com"
	intlOauthPluginVersion    = "1.0.0"
	intlOauthDeviceBrand      = "83DG"
	intlOauthDeviceType       = "windows"
	intlOauthOSVersion        = "Windows 11 Pro"
	intlOauthEnv              = "prod"
	oauthAppVersionDefault    = "3.5.66"
	intlOauthAppType          = "trae"
	intlOauthDeviceName       = "DESKTOP-CPAINTL"
)

var intlversion = "0.1.0"

var (
	intlloginStates sync.Map

	// intlupstreamClient: the identifier `upstream` below is the package
	// name declared INSIDE the intlupstream module (not the CN/SOLO
	// trae/upstream package). Package-var initialization guarantees the
	// client exists before any RPC — v0.12.0-0.12.1 left it nil and the
	// first Intl model.for_auth crashed with a nil-receiver SIGSEGV.
	intlupstreamClient = upstream.New()
)

func intlhandleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return okEnvelope(intlbuildRegistration())

	case pluginabi.MethodModelStatic:
		return intlhandleModelStatic(request)

	case pluginabi.MethodModelForAuth:
		return intlhandleModelForAuth(request)

	case pluginabi.MethodAuthIdentifier:
		return okEnvelope(intlidentifierResponse{Identifier: intlproviderName})

	case pluginabi.MethodAuthParse:
		return intlhandleParseAuth(request)

	case pluginabi.MethodAuthLoginStart:
		return intlhandleStartLogin(request)

	case pluginabi.MethodAuthLoginPoll:
		return intlhandlePollLogin(request)

	case pluginabi.MethodAuthRefresh:
		return intlhandleRefreshAuth(request)

	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(intlidentifierResponse{Identifier: intlproviderName})

	case pluginabi.MethodExecutorExecute:
		return intlhandleExecExecute(request)

	case pluginabi.MethodExecutorExecuteStream:
		return intlhandleExecStream(request)

	case pluginabi.MethodExecutorCountTokens:
		return okEnvelope(pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":0}`)})

	case pluginabi.MethodManagementRegister:
		// Cache host-injected BasePath so intlhandleManagement doesn't hardcode
		// /v0/management (tolerate future host path changes).
		var regReq pluginapi.ManagementRegistrationRequest
		if err := json.Unmarshal(request, &regReq); err == nil {
			if regReq.BasePath != "" {
				intlsetManagementBasePath(regReq.BasePath)
			}
		}
		return okEnvelope(intlmanagementRegistration())

	case pluginabi.MethodManagementHandle:
		return intlhandleManagement(request)

	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

// -----------------------------------------------------------------------------
// Registration
// -----------------------------------------------------------------------------

type intlidentifierResponse struct {
	Identifier string `json:"identifier"`
}

type intlregistrationPayload struct {
	SchemaVersion uint32                     `json:"schema_version"`
	Metadata      pluginapi.Metadata         `json:"metadata"`
	Capabilities  intlregistrationCapability `json:"capabilities"`
}

type intlregistrationCapability struct {
	ModelProvider         bool                         `json:"model_provider"`
	AuthProvider          bool                         `json:"auth_provider"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats,omitempty"`
	ManagementAPI         bool                         `json:"management_api"`
}

func intlbuildRegistration() intlregistrationPayload {
	return intlregistrationPayload{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             intlproviderName,
			Version:          intlversion,
			Author:           "mmqz (based on OmniRoute trae.ts by diegosouzapw)",
			GitHubRepository: "https://github.com/mmqz/cpa-multi-plugins",
			Logo:             pluginLogoURL,
			ConfigFields: []pluginapi.ConfigField{
				{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "Optional model list. Use 'auto' for server-pick, 'work' for fast work mode, or specific model names (gpt-5.2, gemini-3.1-pro, kimi-k2.5, etc)."},
			},
		},
		Capabilities: intlregistrationCapability{
			ModelProvider:         true,
			AuthProvider:          true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeOAuth,
			ExecutorInputFormats:  []string{"chat-completions"},
			ExecutorOutputFormats: []string{"chat-completions"},
			ManagementAPI:         true,
		},
	}
}

// -----------------------------------------------------------------------------
// Models
// -----------------------------------------------------------------------------

func intlhandleModelStatic(_ []byte) ([]byte, error) {
	return okEnvelope(pluginapi.ModelResponse{Provider: intlproviderName, Models: []pluginapi.ModelInfo{}})
}

func intlhandleModelForAuth(request []byte) ([]byte, error) {
	// Host contract: top-level base64 StorageJSON (see handleModelForAuth,
	// v0.12.2 fix — the nested "auth" shape never matched the host request).
	var req struct {
		StorageJSON  []byte                      `json:"StorageJSON"`
		AuthID       string                      `json:"AuthID"`
		AuthProvider string                      `json:"AuthProvider"`
		Metadata     map[string]any              `json:"Metadata"`
		Attributes   map[string]string           `json:"Attributes"`
		Host         pluginapi.HostConfigSummary `json:"Host"`
	}
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, err
	}
	a, err := intlparseStoredAuth(req.StorageJSON)
	if err != nil {
		log.Printf("intl model.for_auth: parse storage failed (%v) — no account catalog", err)
		return okEnvelope(pluginapi.ModelResponse{Provider: intlproviderName, Models: []pluginapi.ModelInfo{}})
	}
	out := intlmodelCatalogForAuth(req.StorageJSON, req.AuthID, a)
	// v0.12.62: the intl namespace honors the provider key plus the
	// "trae-intl" sub-key.
	out = filterExcludedModels(out, req.Host, providerName+"-intl")
	return okEnvelope(pluginapi.ModelResponse{Provider: intlproviderName, Models: out})
}

// intlmodelCatalogForAuth uses the account-scoped daily catalog without rotating
// tokens. Only explicit credential refresh and existing executor paths rotate.
func intlmodelCatalogForAuth(storageJSON []byte, authID string, a *upstream.Auth) []pluginapi.ModelInfo {
	ca, err := parseStoredAuth(storageJSON)
	if err != nil {
		return []pluginapi.ModelInfo{}
	}
	ca.Variant = variantIntl
	return accountModelsForStorage(ca, storageJSON, false, authID)
}

func intlstaticModels() []pluginapi.ModelInfo {
	// "-intl" suffix namespaces Intl models so host routing can never send
	// a CN/SOLO-model request to an Intl credential (or vice versa).
	// "auto"/"work" are Intl-exclusive virtual names and stay unsuffixed.
	known := []string{"auto", "work", "gpt-5.2-intl", "gemini-3.1-pro-intl", "kimi-k2.5-intl", "claude-sonnet-4-5-intl"}
	out := make([]pluginapi.ModelInfo, 0, len(known))
	for _, id := range known {
		out = append(out, pluginapi.ModelInfo{ID: id, Name: id, OwnedBy: intlproviderName})
	}
	return out
}

// -----------------------------------------------------------------------------
// Auth: parse / login / refresh
// -----------------------------------------------------------------------------

// intlResolvedModel returns the model id the intl upstream should see
// (issue #18). The host-resolved executor model wins: the raw body rides
// the client-written name verbatim — host credential prefix ("tr/…")
// included — and resolveMode only strips the plugin's own "-intl"
// suffix, so a prefixed body name would create the SOLO session with an
// unknown model_name. An empty resolved id (hosts that never populate
// ExecutorRequest.Model) falls back to the body name, the pre-0.12.59
// behavior. The mismatch is logged once per distinct body name through
// the CN upstream's shared diagnostic.
func intlResolvedModel(resolved, bodyModel string) string {
	r := strings.TrimSpace(resolved)
	if r == "" {
		return bodyModel
	}
	if b := strings.TrimSpace(bodyModel); b != "" && b != r {
		if strings.TrimSuffix(b, "-intl") != strings.TrimSuffix(r, "-intl") {
			cnupstream.NoteHostPrefixMismatch(b, r, "intl")
		}
	}
	return r
}

func intlparseStoredAuth(raw []byte) (*upstream.Auth, error) {
	// Support both nested ({"auth":{...},"account":{...}}) and flat shapes.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("parse auth: %w", err)
	}
	var nested struct {
		Auth struct {
			AccessToken   string `json:"accessToken"`
			RefreshToken  string `json:"refreshToken"`
			ExpiresAt     int64  `json:"expiresAt"`
			Domain        string `json:"domain"`
			APIHost       string `json:"apiHost"`
			WebID         string `json:"webId"`
			BizUserID     string `json:"bizUserId"`
			UserUniqueID  string `json:"userUniqueId"`
			UserIdentity  string `json:"userIdentity"`
			Scope         string `json:"scope"`
			Tenant        string `json:"tenant"`
			Region        string `json:"region"`
			AppLanguage   string `json:"appLanguage"`
			AppVersion    string `json:"appVersion"`
			RefererOrigin string `json:"refererOrigin"`
			Timezone      string `json:"timezone"`
			// v0.12.72: device identity + key material — previously dropped on
			// parse, so refresh re-persists auth files without them.
			DeviceID         string `json:"deviceId"`
			MachineID        string `json:"machineId"`
			DevicePublicKey  string `json:"devicePublicKey"`
			DevicePrivateKey string `json:"devicePrivateKey"`
		} `json:"auth"`
		Account struct {
			UID          string `json:"uid"`
			EnterpriseID string `json:"enterpriseId"`
			Nickname     string `json:"nickname"`
		} `json:"account"`
	}
	var flat struct {
		AccessToken      string `json:"accessToken"`
		RefreshToken     string `json:"refreshToken"`
		ExpiresAt        int64  `json:"expiresAt"`
		Domain           string `json:"domain"`
		APIHost          string `json:"apiHost"`
		UID              string `json:"uid"`
		EnterpriseID     string `json:"enterpriseId"`
		Nickname         string `json:"nickname"`
		WebID            string `json:"webId"`
		BizUserID        string `json:"bizUserId"`
		UserUniqueID     string `json:"userUniqueId"`
		UserIdentity     string `json:"userIdentity"`
		Scope            string `json:"scope"`
		Tenant           string `json:"tenant"`
		Region           string `json:"region"`
		AppLanguage      string `json:"appLanguage"`
		AppVersion       string `json:"appVersion"`
		RefererOrigin    string `json:"refererOrigin"`
		Timezone         string `json:"timezone"`
		DeviceID         string `json:"deviceId"`
		MachineID        string `json:"machineId"`
		DevicePublicKey  string `json:"devicePublicKey"`
		DevicePrivateKey string `json:"devicePrivateKey"`
	}
	if _, ok := probe["auth"]; ok {
		if err := json.Unmarshal(raw, &nested); err != nil {
			return nil, fmt.Errorf("parse nested auth: %w", err)
		}
		return &upstream.Auth{
			AccessToken:      nested.Auth.AccessToken,
			RefreshToken:     nested.Auth.RefreshToken,
			ExpiresAt:        nested.Auth.ExpiresAt,
			APIHost:          nested.Auth.APIHost,
			Domain:           intlnonEmpty(nested.Auth.Domain, "trae.ai"),
			UID:              nested.Account.UID,
			EnterpriseID:     nested.Account.EnterpriseID,
			Nickname:         nested.Account.Nickname,
			WebID:            nested.Auth.WebID,
			BizUserID:        nested.Auth.BizUserID,
			UserUniqueID:     nested.Auth.UserUniqueID,
			UserIdentity:     nested.Auth.UserIdentity,
			Scope:            nested.Auth.Scope,
			Tenant:           nested.Auth.Tenant,
			Region:           nested.Auth.Region,
			AppLanguage:      nested.Auth.AppLanguage,
			AppVersion:       nested.Auth.AppVersion,
			RefererOrigin:    nested.Auth.RefererOrigin,
			Timezone:         nested.Auth.Timezone,
			DeviceID:         nested.Auth.DeviceID,
			MachineID:        nested.Auth.MachineID,
			DevicePublicKey:  nested.Auth.DevicePublicKey,
			DevicePrivateKey: nested.Auth.DevicePrivateKey,
		}, nil
	}
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil, fmt.Errorf("parse flat auth: %w", err)
	}
	return &upstream.Auth{
		AccessToken:      flat.AccessToken,
		RefreshToken:     flat.RefreshToken,
		ExpiresAt:        flat.ExpiresAt,
		APIHost:          flat.APIHost,
		Domain:           intlnonEmpty(flat.Domain, "trae.ai"),
		UID:              flat.UID,
		EnterpriseID:     flat.EnterpriseID,
		Nickname:         flat.Nickname,
		WebID:            flat.WebID,
		BizUserID:        flat.BizUserID,
		UserUniqueID:     flat.UserUniqueID,
		UserIdentity:     flat.UserIdentity,
		Scope:            flat.Scope,
		Tenant:           flat.Tenant,
		Region:           flat.Region,
		AppLanguage:      flat.AppLanguage,
		AppVersion:       flat.AppVersion,
		RefererOrigin:    flat.RefererOrigin,
		Timezone:         flat.Timezone,
		DeviceID:         flat.DeviceID,
		MachineID:        flat.MachineID,
		DevicePublicKey:  flat.DevicePublicKey,
		DevicePrivateKey: flat.DevicePrivateKey,
	}, nil
}

func intlhandleParseAuth(request []byte) ([]byte, error) {
	// v0.12.4 fix: accept the host wire format (RawJSON base64) — see
	// handleParseAuth for the full rationale.
	var req struct {
		StorageJSON json.RawMessage `json:"storage_json"`
		FileName    string          `json:"FileName"`
		RawJSON     json.RawMessage `json:"RawJSON"`
	}
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, err
	}
	payload, ok := extractAuthPayload(request)
	if !ok {
		payload, ok = req.StorageJSON, len(req.StorageJSON) > 0
	}
	if !ok {
		return errorEnvelope("parse_error", "no storage_json/raw_json payload in auth.parse request"), nil
	}
	a, err := intlparseStoredAuth(payload)
	if err != nil {
		return errorEnvelope("parse_error", err.Error()), nil
	}
	return okEnvelope(pluginapi.AuthParseResponse{
		Handled: true,
		Auth: pluginapi.AuthData{
			// v0.12.8: leave ID empty — host derives it from the file path
			// (same rationale as the cn/solo parse handler).
			Provider:    intlproviderName,
			ID:          "",
			FileName:    intlnonEmpty(req.FileName, intlauthFileName),
			Label:       intlnonEmpty(a.Nickname, "Trae Intl "+a.UID),
			StorageJSON: payload,
			Metadata:    map[string]any{"type": intlproviderName, "uid": a.UID, "note": "INTL"},
		},
	})
}

func intlhandleStartLogin(request []byte) ([]byte, error) {
	// Step 1: PKCE + login_trace_id.
	loginTraceID := newLoginTraceID()
	codeVerifier, codeChallenge := generatePKCEPair()

	// Step 2: Allocate local callback port. The trae.ai authorization page
	// enforces the same client-side callback rule as www.trae.cn (verified
	// 2026-09-02: a LAN-host callback renders "Login Failed/Something went
	// wrong", only http://127.0.0.1:<port>/authorize reaches the login UI),
	// so callback_public_host cannot satisfy the page — always advertise the
	// in-process loopback listener (v0.12.12; parity with main.go).
	host := parseLoginHostContext(request)

	// LOCAL LISTENER ONLY (v0.12.12): the INTL authorization page
	// (www.trae.ai/authorization, live-verified 2026-09-03) hard-validates
	// auth_callback_url against /^http:\/\/127\.0\.0\.1:(\d+)\/authorize$/ —
	// the host resource route (v0.12.4-0.12.11) failed client-side with
	// "Login Failed / Something went wrong." before any API call. See the
	// long comment in main.go startLoginWithVariant.
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", loadedCallbackBind(), loadedCallbackPort()))
	if err != nil {
		return nil, fmt.Errorf("allocate callback port (bind=%s port=%d): %w — with a fixed callback_port another process may be holding it", loadedCallbackBind(), loadedCallbackPort(), err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	cbURL := fmt.Sprintf("http://127.0.0.1:%d/authorize", port)

	// Step 3: POST GetLoginGuidance with multi-endpoint fallback
	// (cockpit-tools request_login_guidance). Intl tries api.marscode.com →
	// api.trae.ai → www.trae.ai, so a single-host 404/outage no longer
	// kills the login flow with "GetLoginGuidance upstream 404".
	loginHost, err := intlrequestLoginGuidance(false, loginTraceID)
	if err != nil {
		closeListener(ln)
		return nil, fmt.Errorf("GetLoginGuidance failed: %w", err)
	}

	// Step 5: Build verification URI with PKCE.
	// v0.12.76→v0.12.77: bootstrap device only — the URI is built before
	// the account is known; the identity registered server-side is
	// derived PER ACCOUNT at the callback (applyDerivedIdentityIntl,
	// device_identity.go). See the main.go counterpart.
	ident := ensureLoginDeviceIdentity(host.AuthDir, variantIntl)
	deviceID, machineID := ident.DeviceID, ident.MachineID
	verificationURI := intlbuildVerificationURI(loginHost, intlverificationURIParams{
		AuthFrom:      oauthAuthFrom,
		PluginVersion: intlOauthPluginVersion,
		ClientID:      intlupstreamClient.ClientID,
		LoginTraceID:  loginTraceID,
		CallbackURL:   cbURL,
		MachineID:     machineID,
		DeviceID:      deviceID,
		DeviceBrand:   intlOauthDeviceBrand,
		DeviceType:    intlOauthDeviceType,
		OSVersion:     intlOauthOSVersion,
		Env:           intlOauthEnv,
		AppVersion:    loadedAppVersion(),
		AppType:       intlOauthAppType,
		CodeChallenge: codeChallenge,
		HideSaasLogin: false, // Intl non-SOLO does not hide SaaS login
	})

	// Step 6: Store login state.
	lc := &intlloginCtx{
		listener:      ln,
		authDir:       host.AuthDir,
		state:         loginTraceID,
		cbURL:         cbURL,
		expires:       time.Now().Add(loginTTL),
		loginTraceID:  loginTraceID,
		codeVerifier:  codeVerifier,
		codeChallenge: codeChallenge,
		deviceID:      deviceID,
		machineID:     machineID,
		// v0.12.76: 登录起点解析出的设备密钥对（稳定身份的一部分），
		// 交换点复用并随凭证落盘。
		devicePublicKey:  ident.DevicePublicKey,
		devicePrivateKey: ident.DevicePrivateKey,
	}

	// v0.12.17: disk-persisted pending login — the paste box keeps working
	// across host restarts / docker bounce (same contract as the cn flow).
	lc.loginHost = loginHost
	lc.Owner = host.Owner
	lc.CreatedAt = time.Now().Unix()
	if err := registerLoginIntl(lc); err != nil {
		closeListener(ln)
		return nil, err
	}

	if ln != nil {
		go intlacceptCallback(loginTraceID)
	}

	return okEnvelope(pluginapi.AuthLoginStartResponse{
		Provider:  intlproviderName,
		URL:       verificationURI,
		State:     loginTraceID,
		ExpiresAt: time.Now().Add(loginTTL).UTC(),
		Metadata:  map[string]any{"stage": "pending", "persistent": host.AuthDir != "", "retryable": true, "logo": pluginLogoURL, "callback_url": cbURL, "fallback_callback_path": resourceCallbackPath, "fallback_submit_path": resourceSubmitPath},
	})
}

func intlhandlePollLogin(request []byte) ([]byte, error) {
	return pollLoginIntl(request)
}

func intlhandleRefreshAuth(request []byte) ([]byte, error) { return refreshAuthRequest(request) }

// -----------------------------------------------------------------------------
// Executor: execute + execute_stream
// -----------------------------------------------------------------------------

func intlhandleExecExecute(request []byte) ([]byte, error) {
	var req pluginapi.ExecutorRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, err
	}
	a, err := intlparseStoredAuth(req.StorageJSON)
	if err != nil {
		return nil, fmt.Errorf("execute: parse auth: %w", err)
	}
	a, err = refreshIntlExecutorAuth(req, a)
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", err)
	}
	// Parse OpenAI messages from payload.
	var openaiReq struct {
		Model    string           `json:"model"`
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(req.Payload, &openaiReq); err != nil {
		return nil, fmt.Errorf("execute: parse payload: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// issue #18: 出站模型优先取宿主解析后的 req.Model——body 里的名字在宿主
	// 配了凭据前缀时带前缀，而 resolveMode 只剥插件自己的 -intl 后缀。
	model := intlResolvedModel(req.Model, openaiReq.Model)
	completion, err := intlupstreamClient.Execute(ctx, a, model, openaiReq.Messages)
	if err != nil {
		return nil, fmt.Errorf("execute: %w", err)
	}
	out, _ := json.Marshal(completion)
	return okEnvelope(pluginapi.ExecutorResponse{Payload: out})
}

func intlhandleExecStream(request []byte) ([]byte, error) {
	// v0.12.30: executorStreamRequest embeds the host's stream_id — needed to
	// decide between async host.stream.emit pumping and the sync slice reply.
	var req executorStreamRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, err
	}
	a, err := intlparseStoredAuth(req.StorageJSON)
	if err != nil {
		return nil, fmt.Errorf("stream: parse auth: %w", err)
	}
	a, err = refreshIntlExecutorAuth(req.ExecutorRequest, a)
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", err)
	}
	var openaiReq struct {
		Model    string           `json:"model"`
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(req.Payload, &openaiReq); err != nil {
		return nil, fmt.Errorf("stream: parse payload: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// issue #18: 同 intlhandleExecExecute——req.Model 优先，body 名字仅作回退。
	model := intlResolvedModel(req.Model, openaiReq.Model)
	reader, err := intlupstreamClient.ExecuteStream(ctx, a, model, openaiReq.Messages)
	if err != nil {
		return nil, fmt.Errorf("stream: %w", err)
	}
	// Read all chunks and split into bare-JSON ExecutorStreamChunks
	// (v0.12.36; was a single raw-body chunk).
	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("stream: read: %w", err)
	}
	chunks := splitSSEBodyChunks(raw)
	// v0.12.30: the RPC envelope carries chunks as a SLICE (rpc_schema.go
	// rpcExecutorStreamResponse). Marshaling pluginapi.ExecutorStreamResponse
	// embedded the receive-only channel and failed with "json: unsupported
	// type: <-chan pluginapi.ExecutorStreamChunk" — INTL streaming 503'd on
	// every call. If the host opened an async stream (stream_id), pump the
	// body through host.stream.emit; otherwise return the collected slice.
	// v0.12.36: each payload is BARE JSON — the host SSE-writer frames every
	// chunk as "data: %s\n\n" (openai_handlers.go), so emitting the raw SSE
	// body produced "data: data: {...}" on the wire and duplicated the
	// trailing [DONE] the host appends itself.
	if req.StreamID != "" {
		go func() {
			for _, chunk := range chunks {
				if err := streamEmit(req.StreamID, chunk.Payload); err != nil {
					return
				}
			}
			streamClose(req.StreamID)
		}()
		return okEnvelope(streamResponse{
			Headers: http.Header{"Content-Type": []string{"text/event-stream"}},
		})
	}
	return okEnvelope(streamResponse{
		Headers: http.Header{"Content-Type": []string{"text/event-stream"}},
		Chunks:  chunks,
	})
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// splitSSEBodyChunks splits a raw OpenAI-compatible SSE body ("data: {...}"
// lines) into bare-JSON ExecutorStreamChunk payloads. Empty lines and the
// upstream's own "data: [DONE]" marker are dropped: the host frames every
// payload as "data: %s\n\n" and appends its final "data: [DONE]" itself
// (openai_handlers.go handleStreamResult). Shared by the INTL stream path
// (v0.12.36 double-prefix fix).
func splitSSEBodyChunks(raw []byte) []pluginapi.ExecutorStreamChunk {
	var chunks []pluginapi.ExecutorStreamChunk
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		content := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if content == "" || content == "[DONE]" {
			continue
		}
		chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: []byte(content)})
	}
	return chunks
}

func intltruncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

func intlnonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) != "" {
		return s
	}
	return fallback
}

func intlnormalizeExpiresAt(v int64) int64 {
	if v > 1e12 {
		return v / 1000
	}
	return v
}

// intlextractLoginHost extracts LoginHost from GetLoginGuidance response.
func intlextractLoginHost(raw []byte) string {
	var probe struct {
		Result struct {
			LoginHost string `json:"LoginHost"`
			LoginURL  string `json:"LoginURL"`
		} `json:"Result"`
		Result2 struct {
			LoginHost string `json:"loginHost"`
			LoginURL  string `json:"loginUrl"`
		} `json:"result"`
		Data struct {
			Result struct {
				LoginHost string `json:"LoginHost"`
			} `json:"Result"`
		} `json:"data"`
		LoginHost string `json:"LoginHost"`
		LoginURL  string `json:"loginUrl"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	if probe.Result.LoginHost != "" {
		return probe.Result.LoginHost
	}
	if probe.Result2.LoginHost != "" {
		return probe.Result2.LoginHost
	}
	if probe.Result.LoginURL != "" {
		return probe.Result.LoginURL
	}
	if probe.Result2.LoginURL != "" {
		return probe.Result2.LoginURL
	}
	if probe.Data.Result.LoginHost != "" {
		return probe.Data.Result.LoginHost
	}
	if probe.LoginHost != "" {
		return probe.LoginHost
	}
	return probe.LoginURL
}

// intlverificationURIParams holds parameters for building the OAuth verification URL.
type intlverificationURIParams struct {
	AuthFrom      string
	PluginVersion string
	ClientID      string
	LoginTraceID  string
	CallbackURL   string
	MachineID     string
	DeviceID      string
	DeviceBrand   string
	DeviceType    string
	OSVersion     string
	Env           string
	AppVersion    string
	AppType       string
	CodeChallenge string
	HideSaasLogin bool
}

// intlbuildVerificationURI constructs the browser-facing OAuth URL.
func intlbuildVerificationURI(loginHost string, p intlverificationURIParams) string {
	// intlensureHTTPSScheme mirrors cockpit-tools ensure_https_url: a scheme-less
	// URL would be resolved as a RELATIVE path by the panel browser and hit
	// the CPA server itself → "404 page not found".
	loginHost = intlensureHTTPSScheme(loginHost)
	u, err := url.Parse(loginHost)
	if err != nil {
		return loginHost
	}
	u.Path = "/authorization"
	q := u.Query()
	q.Set("login_version", "1")
	q.Set("auth_from", p.AuthFrom)
	q.Set("login_channel", "native_ide")
	q.Set("plugin_version", p.PluginVersion)
	q.Set("auth_type", "local")
	q.Set("client_id", p.ClientID)
	q.Set("redirect", "0")
	q.Set("login_trace_id", p.LoginTraceID)
	q.Set("auth_callback_url", p.CallbackURL)
	q.Set("machine_id", p.MachineID)
	q.Set("device_id", p.DeviceID)
	q.Set("x_device_id", p.DeviceID)
	q.Set("x_machine_id", p.MachineID)
	q.Set("x_device_brand", p.DeviceBrand)
	q.Set("x_device_type", p.DeviceType)
	q.Set("x_os_version", p.OSVersion)
	q.Set("x_env", p.Env)
	q.Set("x_app_version", p.AppVersion)
	q.Set("x_app_type", p.AppType)
	q.Set("code_challenge", p.CodeChallenge)
	q.Set("code_challenge_method", "S256")
	if p.HideSaasLogin {
		q.Set("hide_saas_login", "true")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// writeCallbackHTML writes a success/failure HTML page to the HTTP response.

// intlloginCtx holds the local callback listener for one in-flight OAuth flow.
type intlloginCtx struct {
	loginProgress
	listener      net.Listener
	state         string
	cbURL         string
	expires       time.Time
	loginTraceID  string
	codeVerifier  string
	codeChallenge string
	deviceID      string
	machineID     string
	// v0.12.76: 登录起点解析出的设备密钥对（稳定身份的一部分），随凭证落盘。
	devicePublicKey  string
	devicePrivateKey string
	loginHost        string
	// userTag echoes the callback's userTag parameter (v0.12.24, cockpit-tools
	// TraeCallbackPayload.user_tag): USTTP accounts exchange against the US
	// origin (grow-normal.traeapi.us) instead of the SG default.
	userTag  string
	authCode string
	// cbUID/cbNickname: the callback's userInfo identity echo (v0.12.25,
	// cockpit-tools TraeCallbackPayload.userInfo) — stable per-account file
	// identity when GetUserInfo fails (duplicate-account fix, intl parity).
	cbUID      string
	cbNickname string
	err        error
	done       chan struct{}

	// authDir: host auth dir for the .oauth callback-file fallback.
	authDir string
	// restored: re-materialized from the disk pending record (v0.12.17).
	restored bool
	// doneOnce guards done for the resource-callback completion path.
	doneOnce sync.Once
	// selfOnce guards the v0.12.23 grace-based self-completion spawn:
	// exactly one selfCompleteIntlAfterGrace goroutine per login.
	selfOnce sync.Once
}

// intlacceptCallback accepts OAuth callback GET /authorize?... requests until
// the flow completes or the TTL expires. v0.12.3: looping Accept — the
// previous single-Accept version died on browser preconnects, favicon
// probes and retries before the real redirect arrived (same bug class the
// CN/SOLO flow hit and fixed in v0.12.2).
func intlacceptCallback(state string) {
	v, ok := intlloginStates.Load(state)
	if !ok {
		return
	}
	lc := v.(*intlloginCtx)

	ln := lc.listener
	_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(loginTTL))
	for {
		if lc.CreatedAt != 0 && time.Now().After(time.Unix(lc.CreatedAt, 0).Add(loginTTL)) {
			closeListener(lc.listener)
			return
		}
		conn, err := ln.Accept()
		if err != nil {

			closeListener(lc.listener)
			return
		}
		if intlHandleCallbackConn(conn, lc) {
			// v0.12.74：监听器路径与 resource 路由对齐（intl）——回调
			// 已解决流程但宿主 poll 停止抽干时，此前无人推进登录，
			// pending 卡满 15 分钟 TTL 且 janitor 回收不记 outcome
			//（issue #38 实录，cn 侧同修）。selfOnce 保证幂等，grace
			// 语义与 resource 路由完全一致。
			spawnSelfCompleteIntl(lc)
			closeListener(lc.listener)
			return
		}
	}
}

// intlHandleCallbackConn serves ONE callback connection and returns true
// when the OAuth flow is resolved (authCode captured, or the provider
// reported an error). Anything else (favicon, "/", preconnects) gets a 404
// and the listener keeps waiting for the real redirect.
func intlHandleCallbackConn(conn net.Conn, lc *intlloginCtx) bool {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	buf := make([]byte, 16384)
	n, _ := conn.Read(buf)
	req := string(buf[:n])
	// Parse the first HTTP request line: "GET /authorize?... HTTP/1.1"
	firstLine := req
	if nl := strings.Index(req, "\r\n"); nl >= 0 {
		firstLine = req[:nl]
	}
	sp := strings.Index(firstLine, " ")
	if sp < 0 {
		writeCallbackStatus(conn, "400 Bad Request")
		return false
	}
	rest := firstLine[sp+1:]
	// Trim trailing HTTP version (e.g. " HTTP/1.1").
	if sp2 := strings.LastIndex(rest, " "); sp2 >= 0 {
		rest = rest[:sp2]
	}
	q := strings.Index(rest, "?")
	if q < 0 {
		// No query string at all (e.g. "GET / HTTP/1.1", favicon probes).
		writeCallbackStatus(conn, "404 Not Found")
		return false
	}
	vals, _ := url.ParseQuery(rest[q+1:])

	msg, accepted := acceptLoginCallbackIntl(lc, vals)
	if !accepted {
		writeCallbackStatus(conn, "404 Not Found")
		return false
	}
	if msg != "" {
		writeCallbackHTML(conn, "Login failed", msg)
	} else {
		writeCallbackHTML(conn, "Login successful", "Callback received; credentials are being prepared.")
	}
	return true
}
