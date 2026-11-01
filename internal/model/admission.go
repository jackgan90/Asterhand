package model

const TaskConfigurationUnavailable = "task_configuration_unavailable"

// TaskRejection is the durable refusal of this exact request identity. It must
// be committed before being returned as proof that no delayed copy can execute.
// Generic transport/server errors cannot make this claim.
type TaskRejection struct {
	Code          string `json:"code"`
	Message       string `json:"error"`
	RequestID     string `json:"request_id"`
	RequestDigest string `json:"request_digest"`
}

func (e *TaskRejection) Error() string { return e.Message }

func (e *TaskRejection) Matches(r Request) bool {
	return e.Code == TaskConfigurationUnavailable && e.RequestID == r.RequestID && e.RequestDigest == Hash(r)
}
