package main

import (
	"sort"
	"strings"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// thinkingSupportForFacts projects the catalog-declared thinking tiers onto the
// host ModelInfo contract so CPA can advertise the same levels the panel shows.
//
// Only an explicit, non-contradictory declaration is advertised:
//   - the source must belong to this realm's serving lane;
//   - the source must not explicitly report thinking unsupported;
//   - other sources covering the same model must not contradict the level set.
//
// Unknown stays nil rather than inventing tiers the gateway never offered.
func thinkingSupportForFacts(f upstream.ModelFacts, function string) *pluginapi.ThinkingSupport {
	levels := map[string]struct{}{}
	var zeroAllowed *bool
	declared := false
	for _, c := range f.CapabilityCatalog {
		if c.SourceFunction != function {
			continue
		}
		if c.ThinkingSupported != nil && !*c.ThinkingSupported {
			continue
		}
		current := map[string]struct{}{}
		for _, option := range c.ThinkingOptions {
			option = strings.TrimSpace(option)
			if option == "" {
				continue
			}
			current[option] = struct{}{}
		}
		if len(current) == 0 {
			continue
		}
		if declared && !sameThinkingLevels(levels, current) {
			// Two sources for one model disagree; advertise nothing.
			return nil
		}
		levels = current
		declared = true
		if c.ThinkingSupported != nil && *c.ThinkingSupported {
			yes := true
			zeroAllowed = &yes
		}
	}
	if !declared {
		return nil
	}
	out := make([]string, 0, len(levels))
	for level := range levels {
		out = append(out, level)
	}
	sort.Strings(out)
	support := &pluginapi.ThinkingSupport{Levels: out}
	if zeroAllowed != nil {
		support.ZeroAllowed = *zeroAllowed
	}
	return support
}

func sameThinkingLevels(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for level := range a {
		if _, ok := b[level]; !ok {
			return false
		}
	}
	return true
}
