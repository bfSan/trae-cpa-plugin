package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// loginExchangeFailure contains only allowlisted diagnostics. Never retain an
// upstream message, URL, request body or transport error in this public value.
// The category describes the observation, NOT whether the code was consumed.
// None of these failures authorize another POST of the same one-time code.
type loginExchangeFailure struct {
	Category   string `json:"category"`
	HTTPStatus int    `json:"http_status,omitempty"`
	BizCode    string `json:"biz_code,omitempty"`
}

func (e *loginExchangeFailure) sanitized() *loginExchangeFailure {
	if e == nil {
		return nil
	}
	out := &loginExchangeFailure{Category: "outcome_uncertain"}
	switch e.Category {
	case "request_unavailable", "transport_uncertain", "response_unavailable", "http_error", "business_rejected", "invalid_response", "missing_access_token", "outcome_uncertain":
		out.Category = e.Category
	}
	if e.HTTPStatus >= 100 && e.HTTPStatus <= 599 {
		out.HTTPStatus = e.HTTPStatus
	}
	if safeLoginCode(e.BizCode) != "" {
		out.BizCode = e.BizCode
	}
	return out
}

func (e *loginExchangeFailure) Error() string {
	safe := e.sanitized()
	message := "login exchange failed (" + safe.Category + ")"
	if safe.HTTPStatus != 0 {
		message += fmt.Sprintf(" HTTP %d", safe.HTTPStatus)
	}
	return message + safeLoginCode(safe.BizCode) + loginExchangeErrorHint(safe.BizCode) + "; start a new login (code will not be resubmitted)"
}

// exchangeAuthCodeOnce intentionally uses only the selected first origin.
// Even an HTTP 404 can be returned after a request reaches upstream; without
// an explicit non-consumption contract it is not a safe replay signal.
func exchangeAuthCodeOnce(urls []string, body []byte) ([]byte, error) {
	fail := func(category string, status int, code string) ([]byte, error) {
		return nil, (&loginExchangeFailure{Category: category, HTTPStatus: status, BizCode: code}).sanitized()
	}
	if len(urls) == 0 {
		return fail("request_unavailable", 0, "")
	}
	req, err := http.NewRequest(http.MethodPost, urls[0], bytes.NewReader(body))
	if err != nil {
		return fail("request_unavailable", 0, "")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-cloudide-token", "")
	req.Header.Set("User-Agent", "Trae/"+oauthPluginVersion)
	ctx, cancel := context.WithTimeout(req.Context(), 30*time.Second)
	defer cancel()
	resp, err := oauthHTTPDo(req.WithContext(ctx))
	if err != nil {
		return fail("transport_uncertain", 0, "")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, oauthPendingMaxBytes+1))
	if err != nil || len(data) > oauthPendingMaxBytes {
		return fail("response_unavailable", resp.StatusCode, "")
	}
	code, _ := upstreamErrorCode(data)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		category := "http_error"
		if exchangeBusinessRejected(data) {
			category = "business_rejected"
		}
		return fail(category, resp.StatusCode, code)
	}
	// HTTP success does not imply business success. In particular, never accept
	// a token from an envelope that also explicitly reports an upstream error.
	if exchangeBusinessRejected(data) {
		return fail("business_rejected", resp.StatusCode, code)
	}
	if !json.Valid(data) {
		return fail("invalid_response", resp.StatusCode, "")
	}
	if extractExchangeAccessToken(data) == "" {
		return fail("missing_access_token", resp.StatusCode, "")
	}
	return data, nil
}

func exchangeBusinessRejected(data []byte) bool {
	var env struct {
		ResponseMetadata struct {
			Error json.RawMessage `json:"Error"`
		} `json:"ResponseMetadata"`
	}
	if json.Unmarshal(data, &env) != nil {
		return false
	}
	raw := bytes.TrimSpace(env.ResponseMetadata.Error)
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte("{}"))
}

// exchangeTokenFields selects the same envelope for both the preliminary
// validation and final token checkpoint. Otherwise a successful exchange can
// consume the code while the following parse silently discards its token.
func exchangeTokenFields(raw []byte) map[string]any {
	var top map[string]any
	if json.Unmarshal(raw, &top) != nil {
		return nil
	}
	for _, name := range []string{"Result", "result", ""} {
		r := top
		if name != "" {
			r, _ = top[name].(map[string]any)
		}
		for _, key := range []string{"AccessToken", "accessToken", "Token", "token"} {
			if jsonString(r[key]) != "" {
				return r
			}
		}
	}
	return nil
}

func loginProgressExchangeFailure(p *loginProgress) *loginExchangeFailure {
	if p.Token != nil || p.Result != nil || !p.ExchangeStarted {
		return nil
	}
	if p.ExchangeFailure != nil {
		return p.ExchangeFailure.sanitized()
	}
	if p.Failure != "" {
		return &loginExchangeFailure{Category: "outcome_uncertain"}
	}
	return nil
}
