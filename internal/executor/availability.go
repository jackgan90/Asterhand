package executor

import (
	"errors"
	"os"
	"strings"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/i18n"
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
	Available   bool              `json:"available"`
	Code        AvailabilityCode  `json:"code"`
	Detail      string            `json:"detail"`
	MessageKey  string            `json:"message_key,omitempty"`
	MessageArgs map[string]string `json:"message_args,omitempty"`
}

func (a Availability) Localize(locale string) Availability {
	out := a
	out.MessageArgs = i18n.CloneArgs(a.MessageArgs)
	if a.MessageKey != "" {
		out.Detail = i18n.LocalizeMessage(locale, a.MessageKey, a.MessageArgs, a.Detail)
	}
	return out
}

type ConfigurationCapabilities struct {
	Chat       Availability `json:"chat"`
	ModelTasks Availability `json:"model_tasks"`
}

func (c ConfigurationCapabilities) Localize(locale string) ConfigurationCapabilities {
	return ConfigurationCapabilities{
		Chat:       c.Chat.Localize(locale),
		ModelTasks: c.ModelTasks.Localize(locale),
	}
}

type ConfigurationError struct{ Availability Availability }

func (e *ConfigurationError) Error() string {
	if e == nil {
		return ""
	}
	return e.Availability.Localize(i18n.Effective()).Detail
}

func (e *ConfigurationError) I18nKey() (string, map[string]string) {
	if e == nil {
		return "", nil
	}
	return e.Availability.MessageKey, i18n.CloneArgs(e.Availability.MessageArgs)
}

func unavailable(code AvailabilityCode, key string, args ...map[string]string) Availability {
	var params map[string]string
	if len(args) > 0 {
		params = i18n.CloneArgs(args[0])
	}
	return Availability{
		Code:        code,
		Detail:      i18n.T(i18n.Effective(), key, params),
		MessageKey:  key,
		MessageArgs: params,
	}
}

// ConfiguredCapabilities reads one selection and credential for both features.
// It must never start a process, call a model, or enter the task/store locks.
func ConfiguredCapabilities(manager *config.Manager, c config.Config) ConfigurationCapabilities {
	var common Availability
	if err := ValidateSelection(c); err != nil {
		common = unavailable(InvalidConfiguration, "status.availability.invalid_config")
	} else if key, err := manager.Credential(c.LLM.CredentialRef); errors.Is(err, os.ErrNotExist) || err == nil && strings.TrimSpace(key) == "" {
		common = unavailable(CredentialMissing, "status.availability.credential_missing")
	} else if err != nil {
		common = unavailable(CredentialUnreadable, "status.availability.credential_unreadable")
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
