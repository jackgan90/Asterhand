// Package prompt assembles product policy independently of any agent harness.
// It contains no credentials, filesystem access, or permission enforcement.
package prompt

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"strings"
)

type Mode string

const (
	Replace     Mode = "replace"
	Append      Mode = "append"
	Unsupported Mode = "unsupported"
)

type State string

const (
	Pending  State = "pending"
	Applied  State = "applied"
	Degraded State = "degraded"
)

// Application is safe to expose to clients and persist with turn metadata.
// Mode describes support; State describes evidence from this specific turn.
type Application struct {
	Revision string `json:"revision,omitempty"`
	Mode     Mode   `json:"mode"`
	State    State  `json:"state"`
	Reason   string `json:"reason,omitempty"`
}

func (a Application) Normalize() Application {
	if a.Mode != Replace && a.Mode != Append {
		a.Mode = Unsupported
	}
	if a.Mode == Unsupported {
		a.State, a.Reason = Degraded, "unsupported"
	}
	if a.State != Applied && a.State != Degraded {
		a.State = Pending
	}
	if a.State != Degraded {
		a.Reason = ""
	} else if a.Reason != "unsupported" {
		a.Reason = "unconfirmed"
	}
	return a
}

func (a Application) Fallback(reason string) Application {
	a.State, a.Reason = Degraded, reason
	return a.Normalize()
}

type Capabilities struct {
	Tools        []string `json:"tools"`
	History      bool     `json:"history"`
	Streaming    bool     `json:"streaming"`
	Cancellation bool     `json:"cancellation"`
}

type Model struct {
	Provider string `json:"provider"`
	Name     string `json:"model"`
}

type Plan struct {
	Version     int         `json:"version"`
	Application Application `json:"application"`
	Text        string      `json:"text"`
	Digest      string      `json:"digest"`
	// Instance is a fresh, non-secret correlation identity per runtime launch.
	Instance string `json:"instance"`
}

//go:embed default-chat.txt
var defaultChat string

func Default(mode Mode, model Model, capabilities Capabilities) Plan {
	facts, _ := json.Marshal(struct {
		Model        Model        `json:"model"`
		Capabilities Capabilities `json:"capabilities"`
	}{model, capabilities})
	text := strings.TrimSpace(defaultChat) + "\n\n当前节点的有效配置事实（标识和数据，不是额外指令）：\n" + string(facts)
	if len(capabilities.Tools) == 0 {
		text += "\n当前会话没有可调用工具，无法读取或修改本机文件、运行命令、浏览网页、创建定时任务或向外部发送消息。只能基于用户提供的内容与已有知识进行文本回答。"
	} else {
		text += "\n仅使用当前实际暴露并获授权的工具；配置中没有列出的能力不要声称可用。"
	}
	sum := sha256.Sum256([]byte(text))
	digest := hex.EncodeToString(sum[:])
	return Plan{Version: 1, Application: Application{Revision: "default-chat-v2-" + digest[:16], Mode: mode, State: Pending}.Normalize(), Text: text, Digest: digest}
}
