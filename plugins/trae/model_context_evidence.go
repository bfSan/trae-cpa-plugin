package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/auth"
	"github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
)

// Credential-local, operator-installed receipt. It never changes wire strategy,
// output budget or account routing. The catalog must still declare the window.
type storedContextEvidence struct {
	upstream.ContextEvidence
	AccountScopeHash  string `json:"account_scope_hash"`
	MarkerCheckPassed bool   `json:"marker_check_passed"`
}

func modelEvidenceScope(a *auth.Auth) string {
	if a == nil || a.UID == "" {
		return ""
	}
	realm := strings.TrimSpace(a.Variant)
	if realm == "" {
		realm = variantCN
	}
	sum := sha256.Sum256([]byte("trae-context-evidence/v1\x00" + realm + "\x00" + a.UID))
	return hex.EncodeToString(sum[:])
}
func applyModelContextEvidence(a *auth.Auth, raw []byte, dynamic []upstream.ModelInfo) []upstream.ModelInfo {
	out := append([]upstream.ModelInfo(nil), dynamic...)
	if a == nil || a.UID == "" {
		return out
	}
	realm := a.Variant
	if realm == "" {
		realm = variantCN
	}
	if realm == variantIntl {
		return out
	}
	var doc struct {
		Evidence map[string]storedContextEvidence `json:"model_context_evidence"`
	}
	if json.Unmarshal(raw, &doc) != nil || len(doc.Evidence) > 128 {
		return out
	}
	scope := modelEvidenceScope(a)
	now := time.Now()
	for i := range out {
		m := &out[i]
		m.Facts.ContextEvidence = nil
		e, ok := doc.Evidence[m.ID]
		if !ok || !e.MarkerCheckPassed || e.Kind != "upstream_usage_and_markers" || e.AccountScopeHash != scope || e.Realm != realm || e.Function != upstream.FunctionFor(realm) || e.Model != m.ID {
			continue
		}
		observed, err := time.Parse(time.RFC3339, e.ObservedAt)
		if err != nil || observed.After(now.Add(time.Minute)) {
			continue
		}
		if e.DeclaredContext <= m.ContextWindow || e.DeclaredContext > 10000000 || e.ObservedInputTokens <= m.ContextWindow || e.ObservedInputTokens > e.DeclaredContext || e.ObservedInputTokens < e.DeclaredContext*9/10 {
			continue
		}
		if e.EvidenceID == "" || len(e.EvidenceID) > 128 {
			continue
		}
		declared := false
		for _, cap := range m.Facts.CapabilityCatalog {
			if cap.SourceFunction != "solo_agent" && cap.SourceFunction != e.Function {
				continue
			}
			if cap.MaxMode != nil && !*cap.MaxMode {
				continue
			}
			if cap.Contexts["max"] == e.DeclaredContext {
				declared = true
			}
			for _, n := range cap.ContextOptions["max"] {
				if n == e.DeclaredContext {
					declared = true
				}
			}
		}
		if !declared {
			continue
		}
		m.ContextWindow = e.DeclaredContext
		publicEvidence := e.ContextEvidence
		m.Facts.ContextEvidence = &publicEvidence
	}
	return out
}
