package model

import "github.com/jackgan90/Asterhand/internal/i18n"

const TaskConfigurationUnavailable = "task_configuration_unavailable"

// TaskRejection is the durable refusal of this exact request identity. It must
// be committed before being returned as proof that no delayed copy can execute.
// Generic transport/server errors cannot make this claim.
type TaskRejection struct {
	Code          string            `json:"code"`
	Message       string            `json:"error"`
	MessageKey    string            `json:"message_key,omitempty"`
	MessageArgs   map[string]string `json:"message_args,omitempty"`
	RequestID     string            `json:"request_id"`
	RequestDigest string            `json:"request_digest"`
}

func (e *TaskRejection) Error() string {
	if e == nil {
		return ""
	}
	return i18n.LocalizeMessage(i18n.Effective(), e.MessageKey, e.MessageArgs, e.Message)
}

func (e *TaskRejection) I18nKey() (string, map[string]string) {
	if e == nil {
		return "", nil
	}
	return e.MessageKey, i18n.CloneArgs(e.MessageArgs)
}

func (e *TaskRejection) Matches(r Request) bool {
	return e.Code == TaskConfigurationUnavailable && e.RequestID == r.RequestID && e.RequestDigest == Hash(r)
}
