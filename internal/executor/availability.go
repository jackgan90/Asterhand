package executor

import (
	"errors"
	"os"
	"strings"

	"github.com/jackgan90/Asterhand/internal/config"
)

type AvailabilityCode string

const (
	ConfigurationAvailable AvailabilityCode = "configured"
	InvalidConfiguration   AvailabilityCode = "invalid_configuration"
	CredentialMissing      AvailabilityCode = "credential_missing"
	CredentialUnreadable   AvailabilityCode = "credential_unreadable"
	RuntimeMissing         AvailabilityCode = "runtime_missing"
	RuntimeIncompatible    AvailabilityCode = "runtime_incompatible"
	TaskToolsMissing       AvailabilityCode = "task_tools_missing"
)

// Availability describes local prerequisites, not a live model health check.
type Availability struct {
	Available bool             `json:"available"`
	Code      AvailabilityCode `json:"code"`
	Detail    string           `json:"detail"`
}

type ConfigurationCapabilities struct {
	Chat       Availability `json:"chat"`
	ModelTasks Availability `json:"model_tasks"`
}

type ConfigurationError struct{ Availability Availability }

func (e *ConfigurationError) Error() string { return e.Availability.Detail }

func unavailable(code AvailabilityCode, detail string) Availability {
	return Availability{Code: code, Detail: detail}
}

// ConfiguredCapabilities reads one selection and credential for both features.
// It must never start a process, call a model, or enter the task/store locks.
func ConfiguredCapabilities(manager *config.Manager, c config.Config) ConfigurationCapabilities {
	var common Availability
	if err := ValidateSelection(c); err != nil {
		common = unavailable(InvalidConfiguration, "模型或运行时配置无效，请检查本机节点设置")
	} else if key, err := manager.Credential(c.LLM.CredentialRef); errors.Is(err, os.ErrNotExist) || err == nil && strings.TrimSpace(key) == "" {
		common = unavailable(CredentialMissing, "请在本机节点设置中配置模型 API key")
	} else if err != nil {
		common = unavailable(CredentialUnreadable, "模型凭据无法读取，请在本机节点设置中重新保存 API key")
	} else {
		adapter, _ := RuntimeFor(c.AgentRuntime, manager) // ValidateSelection resolved it.
		return adapter.Configuration(c)
	}
	return ConfigurationCapabilities{Chat: common, ModelTasks: common}
}

func RequireTaskConfiguration(manager *config.Manager, c config.Config) error {
	status := ConfiguredCapabilities(manager, c).ModelTasks
	if !status.Available {
		return &ConfigurationError{Availability: status}
	}
	return nil
}
