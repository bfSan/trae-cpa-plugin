package main

import (
	"strings"
	"testing"
)

// v0.12.18: the unified panel's "切换到 Trae Intl 账号面板 →" link pointed at
// the /intl_panel resource path, which — since the v0.12.2 merge — serves the
// SAME unified panel.html. Clicking it reloaded an identical page, which users
// read as "click = refresh". The link is gone (Intl accounts use chips in the unified
// list and carry INTL badges), and the legacy
// intl_panel.html page (only reachable on split-binary deployments serving the
// trae-intl.so build) now links back to the trae plugin's panel via an
// ABSOLUTE cross-plugin path — its old relative href="panel" resolved against
// /v0/resource/plugins/trae-intl/panel and reloaded itself.
func TestPanelNoDeadIntlSwitchLink(t *testing.T) {
	if strings.Contains(string(panelHTML), `href="intl_panel"`) {
		t.Errorf("unified panel still carries the dead self-alias switch link (click = reload)")
	}
	html := string(panelHTML)
	for _, required := range []string{`id="accountFilters"`, `['intl','Intl']`, `isIntl?"INTL"`, "Intl 不提供签到", `if(!isIntl) actions+=`} {
		if !strings.Contains(html, required) {
			t.Errorf("unified panel must retain Intl filtering, identification and no-checkin rule; missing %q", required)
		}
	}
}

func TestIntlPanelSwitchLinkIsAbsolute(t *testing.T) {
	html := string(intlpanelHTML)
	if strings.Contains(html, `href="panel"`) {
		t.Errorf("intl panel switch link must not be relative (self-reload on trae-intl deployments)")
	}
	if !strings.Contains(html, `href="/v0/resource/plugins/trae/panel"`) {
		t.Errorf("intl panel must link the trae plugin panel by absolute path")
	}
}
