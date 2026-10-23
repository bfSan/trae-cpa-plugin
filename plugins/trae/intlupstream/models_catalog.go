package upstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type catalogName struct {
	Name string `json:"name"`
}

// /models has both legacy data[] and current data.list[].models shapes.
// Do not recursively walk arbitrary objects or turn business errors into models.
func parseModelsCatalog(body []byte) ([]string, error) {
	var env struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &env) != nil {
		return nil, fmt.Errorf("models response is not a valid envelope")
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("models api code %d", env.Code)
	}
	data := bytes.TrimSpace(env.Data)
	var entries []catalogName
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil, fmt.Errorf("models api returned empty list")
	}
	switch data[0] {
	case '[':
		if json.Unmarshal(data, &entries) != nil {
			return nil, fmt.Errorf("models list has invalid entries")
		}
	case '{':
		var grouped struct {
			List []struct {
				Function string        `json:"function"`
				Models   []catalogName `json:"models"`
			} `json:"list"`
		}
		if json.Unmarshal(data, &grouped) != nil || grouped.List == nil {
			return nil, fmt.Errorf("models grouped list has an unsupported shape")
		}
		if len(grouped.List) > 128 {
			return nil, fmt.Errorf("models group count exceeds limit")
		}
		for _, g := range grouped.List {
			if len(entries)+len(g.Models) > 4096 {
				return nil, fmt.Errorf("models count exceeds limit")
			}
			entries = append(entries, g.Models...)
		}
	default:
		return nil, fmt.Errorf("models data has an unsupported shape")
	}
	if len(entries) > 4096 {
		return nil, fmt.Errorf("models count exceeds limit")
	}
	out := make([]string, 0, len(entries))
	seen := map[string]bool{}
	for _, entry := range entries {
		n := strings.TrimSpace(entry.Name)
		if n == "" {
			continue
		}
		if len(n) > 256 || strings.ContainsAny(n, "\x00\r\n") {
			return nil, fmt.Errorf("models list contains invalid name")
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("models api returned empty list")
	}
	return out, nil
}
