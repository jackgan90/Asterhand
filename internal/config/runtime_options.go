package config

import (
	"encoding/json"
	"maps"
)

func cloneOptions(options map[string]json.RawMessage) map[string]json.RawMessage {
	if options == nil {
		return nil
	}
	copy := make(map[string]json.RawMessage, len(options))
	for id, value := range options {
		copy[id] = append(json.RawMessage(nil), value...)
	}
	return copy
}
func (c *Config) SetRuntimeOptions(id string, value json.RawMessage) {
	c.RuntimeOptions = cloneOptions(c.RuntimeOptions)
	if c.RuntimeOptions == nil {
		c.RuntimeOptions = map[string]json.RawMessage{}
	}
	c.RuntimeOptions[id] = append(json.RawMessage(nil), value...)
}

func (c *Config) MarkRuntimeOptionsExplicit(id string, explicit bool) {
	c.RuntimeOptionsExplicit = maps.Clone(c.RuntimeOptionsExplicit)
	if c.RuntimeOptionsExplicit == nil {
		c.RuntimeOptionsExplicit = map[string]bool{}
	}
	c.RuntimeOptionsExplicit[id] = explicit
}
