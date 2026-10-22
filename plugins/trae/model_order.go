package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

var modelOrderState struct {
	sync.RWMutex
	ids []string
}

// configureModelOrder consumes host-persisted config. Absence preserves the
// current order for partial updates; an explicit [] resets it.
func configureModelOrder(configYAML []byte) error {
	var cfg struct {
		Order yaml.Node `yaml:"model_order"`
	}
	if err := yaml.Unmarshal(configYAML, &cfg); err != nil {
		return err
	}
	if cfg.Order.Kind == 0 {
		return nil
	}
	if cfg.Order.Kind != yaml.SequenceNode {
		return fmt.Errorf("model_order must be an array of model IDs")
	}
	ids := make([]string, 0, len(cfg.Order.Content))
	seen := map[string]bool{}
	for _, n := range cfg.Order.Content {
		if n.Kind != yaml.ScalarNode || n.Tag != "!!str" || strings.TrimSpace(n.Value) == "" {
			return fmt.Errorf("model_order entries must be non-empty strings")
		}
		id := strings.TrimSpace(n.Value)
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	modelOrderState.Lock()
	modelOrderState.ids = ids
	modelOrderState.Unlock()
	return nil
}

func currentModelOrder() []string {
	modelOrderState.RLock()
	defer modelOrderState.RUnlock()
	return append([]string{}, modelOrderState.ids...)
}

func catalogBaseID(id, realm string) string {
	switch realm {
	case variantSolo:
		return strings.TrimSuffix(id, modelSuffixSolo)
	case variantIntl:
		return strings.TrimSuffix(id, modelSuffixIntl)
	}
	return id
}

// applyModelOrder only reorders existing entries. It never grants a model to an
// account, changes the channel suffix, or manufactures upstream metadata.
func applyModelOrder(models []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	order := currentModelOrder()
	out := append([]pluginapi.ModelInfo{}, models...)
	rank := make(map[string]int, len(order))
	for i, id := range order {
		rank[id] = i
	}
	key := func(id string) int {
		if i, ok := rank[id]; ok {
			return i
		}
		for _, realm := range []string{variantSolo, variantIntl} {
			if i, ok := rank[catalogBaseID(id, realm)]; ok {
				return i
			}
		}
		return len(order)
	}
	sort.SliceStable(out, func(i, j int) bool { return key(out[i].ID) < key(out[j].ID) })
	return out
}
