package main

import (
	"sort"
	"strings"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// hostThinkingLevels is the vocabulary CPA accepts for ModelInfo.Thinking.
// Upstream Trae catalogs name tiers light/high/extra_high; CPA only understands
// these names, so the mapping is explicit rather than passing raw strings that
// the host would silently drop.
var hostThinkingLevels = map[string]string{
	"light":      "low",
	"low":        "low",
	"medium":     "medium",
	"high":       "high",
	"extra_high": "xhigh",
	"xhigh":      "xhigh",
	"max":        "max",
}

func normalizeHostThinkingLevel(level string) string {
	return hostThinkingLevels[strings.ToLower(strings.TrimSpace(level))]
}

// thinkingSupportForFacts projects catalog-declared thinking tiers onto the host
// ModelInfo contract.
//
// Trae's serving lane (solo_work_lite) carries pricing and the callable model
// list but frequently omits reasoning metadata; the same account's auxiliary
// catalog (solo_agent) is where the tier declaration lives. Reading both is
// safe because the model ID was already intersected with the primary list, so
// this only adds capability to a model the serving lane already authorizes.
//
// The serving lane wins when it declares tiers; otherwise the auxiliary catalog
// supplies them. Unknown stays nil and an explicit thinking_supported=false
// suppresses that source.
func thinkingSupportForFacts(f upstream.ModelFacts, variant string) *pluginapi.ThinkingSupport {
	// The serving lane is authoritative when it declares tiers; otherwise the
	// auxiliary catalog supplies them. Sources are not intersected: the serving
	// lane deliberately publishes a narrower set, and intersecting would erase
	// tiers the account can actually use. A source that explicitly denies
	// thinking still suppresses its own declaration.
	var chosen []string
	var zeroAllowed *bool
	consider := func(want string) bool {
		for _, c := range f.CapabilityCatalog {
			if c.SourceFunction != want {
				continue
			}
			if c.ThinkingSupported != nil && !*c.ThinkingSupported {
				continue
			}
			current := map[string]struct{}{}
			for _, option := range c.ThinkingOptions {
				if host := normalizeHostThinkingLevel(option); host != "" {
					current[host] = struct{}{}
				}
			}
			if len(current) == 0 {
				continue
			}
			chosen = chosen[:0]
			for level := range current {
				chosen = append(chosen, level)
			}
			zeroAllowed = nil
			if c.ThinkingSupported != nil && *c.ThinkingSupported {
				yes := true
				zeroAllowed = &yes
			}
			return true
		}
		return false
	}
	serving := upstream.FunctionFor(variant)
	if !consider(serving) {
		for _, c := range f.CapabilityCatalog {
			if c.SourceFunction != serving && consider(c.SourceFunction) {
				break
			}
		}
	}
	declared := len(chosen) > 0
	if !declared {
		return nil
	}
	out := append([]string(nil), chosen...)
	sort.Strings(out)
	support := &pluginapi.ThinkingSupport{Levels: out}
	if zeroAllowed != nil {
		support.ZeroAllowed = *zeroAllowed
	}
	return support
}
