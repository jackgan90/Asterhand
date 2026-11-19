package config

import (
	"strings"

	"github.com/jackgan90/Asterhand/internal/model"
)

func cloneTrustRules(rules []TrustRule) []TrustRule {
	if len(rules) == 0 {
		return nil
	}
	return append([]TrustRule(nil), rules...)
}

func normalizeTrustRules(rules []TrustRule) []TrustRule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]TrustRule, 0, len(rules))
	seen := make(map[string]bool, len(rules))
	for _, r := range rules {
		r.ID = strings.TrimSpace(r.ID)
		r.Kind = strings.ToLower(strings.TrimSpace(r.Kind))
		r.Pattern = strings.TrimSpace(r.Pattern)
		r.Action = strings.ToLower(strings.TrimSpace(r.Action))
		r.Label = strings.TrimSpace(r.Label)
		if r.ID == "" || r.Pattern == "" {
			continue
		}
		if r.Kind != "folder" && r.Kind != "command" {
			continue
		}
		if r.Action != "allow" && r.Action != "ask" && r.Action != "deny" {
			continue
		}
		key := r.Kind + "\x00" + strings.ToLower(r.Pattern) + "\x00" + r.Action
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// UpdateLocalToolsTrust atomically updates ExecutionMode and/or TrustRules under
// writeMu while keeping LocalToolPolicy.Enabled and Revision untouched so the
// managed conversation Gateway does not restart.
func (m *Manager) UpdateLocalToolsTrust(fn func(cur LocalToolPolicy) (LocalToolPolicy, error)) (LocalToolPolicy, error) {
	saved, err := m.Update(func(c Config) (Config, error) {
		cur := c.LocalTools
		cur.TrustRules = cloneTrustRules(cur.TrustRules)
		next, err := fn(cur)
		if err != nil {
			return Config{}, err
		}
		next.Enabled = cur.Enabled
		next.Revision = cur.Revision
		next.TrustRevision = model.ID()
		if next.ExecutionMode != ExecutionModeTurbo {
			next.ExecutionMode = ExecutionModeStandard
		}
		next.TrustRules = normalizeTrustRules(next.TrustRules)
		c.LocalTools = next
		return c, nil
	})
	if err != nil {
		return LocalToolPolicy{}, err
	}
	res := saved.LocalTools
	res.TrustRules = cloneTrustRules(res.TrustRules)
	return res, nil
}
