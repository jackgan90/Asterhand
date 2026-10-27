package executor

import (
	"embed"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/prompt"
)

const chatPromptPlugin = "asterhand-chat-policy"

//go:embed promptplugin/*
var chatPromptFiles embed.FS

func conversationPrompt(c config.Config) prompt.Plan {
	return prompt.Default(prompt.Append, prompt.Model{Provider: c.LLM.Provider, Name: c.LLM.Model},
		prompt.Capabilities{Tools: []string{}, History: true, Streaming: true, Cancellation: true})
}

// Policy files are derived installation artifacts in the protected chat runtime.
// Failure disables only this optional plugin; the native conversation still runs.
func prepareConversationPrompt(c config.Config, root string) (prompt.Plan, bool) {
	plan := conversationPrompt(c)
	plan.Instance = model.ID()
	if err := os.MkdirAll(filepath.Join(root, "prompt-plugin"), 0700); err != nil {
		return plan, false
	}
	files, err := chatPromptFiles.ReadDir("promptplugin")
	if err != nil {
		return plan, false
	}
	for _, file := range files {
		data, err := chatPromptFiles.ReadFile("promptplugin/" + file.Name())
		if err != nil || config.AtomicFile(filepath.Join(root, "prompt-plugin", file.Name()), data, 0600) != nil {
			return plan, false
		}
	}
	data, err := json.Marshal(plan)
	if err != nil || config.AtomicFile(filepath.Join(root, "prompt-policy.json"), data, 0600) != nil {
		return plan, false
	}
	return plan, true
}

func configureConversationPrompt(value map[string]any, root string, enabled bool) {
	if !enabled {
		return
	}
	plugins := value["plugins"].(map[string]any)
	plugins["allow"] = append(plugins["allow"].([]string), chatPromptPlugin)
	plugins["load"].(map[string]any)["paths"] = append(plugins["load"].(map[string]any)["paths"].([]string), filepath.Join(root, "prompt-plugin"))
	plugins["entries"].(map[string]any)[chatPromptPlugin] = map[string]any{"enabled": true,
		"hooks":  map[string]any{"allowConversationAccess": true, "allowPromptInjection": true},
		"config": map[string]any{"policyPath": filepath.Join(root, "prompt-policy.json")}}
}
