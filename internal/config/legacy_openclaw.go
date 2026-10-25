package config

import (
	"encoding/json"
	"github.com/jackgan90/Asterhand/internal/llm"
)

// Only this compatibility reader knows the old provider-specific flat fields.
// New configuration and runtime selection never use these names.
func (c *Config) UnmarshalJSON(data []byte) error {
	type plain Config
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = Config(decoded)
	if c.Model != "" && c.LLM.Provider == "" && c.LLM.Model == "" {
		c.LLM = llm.Selection{Provider: "deepseek", Model: c.Model, CredentialRef: "deepseek"}
		if c.AgentRuntime == "" {
			c.AgentRuntime = "openclaw"
		}
	}
	var legacy struct {
		Node     string `json:"node_path"`
		Entry    string `json:"openclaw_path"`
		Plugin   string `json:"plugin_path"`
		Provider string `json:"provider_path"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return err
	}
	if legacy.Node == "" && legacy.Entry == "" && legacy.Plugin == "" && legacy.Provider == "" {
		return nil
	}
	if c.AgentRuntime == "" {
		c.AgentRuntime = "openclaw"
	}
	if _, exists := c.RuntimeOptions["openclaw"]; !exists {
		provider := c.LLM.Provider
		if provider == "" {
			provider = "deepseek"
		}
		options, err := json.Marshal(map[string]any{"node_path": legacy.Node, "entry_path": legacy.Entry, "plugin_path": legacy.Plugin, "provider_paths": map[string]string{provider: legacy.Provider}})
		if err != nil {
			return err
		}
		c.SetRuntimeOptions("openclaw", options)
	}
	return nil
}
