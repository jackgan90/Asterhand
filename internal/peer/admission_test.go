package peer

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackgan90/Asterhand/internal/model"
)

func TestOnlyCompleteSubmissionRefusalProvesNonAcceptance(t *testing.T) {
	m := manager(t)
	request := model.Request{RequestID: model.ID(), Goal: "fixture", Mode: "execute"}
	rejection := model.TaskRejection{Code: model.TaskConfigurationUnavailable, Message: "missing configuration", RequestID: request.RequestID, RequestDigest: model.Hash(request)}
	encoded, err := json.Marshal(rejection)
	if err != nil {
		t.Fatal(err)
	}
	valid := string(encoded)
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
		definite                 bool
	}{
		{"matching", "POST", "/v1/jobs", valid, 409, true},
		{"cancel", "POST", "/v1/cancel-request", valid, 409, false},
		{"query", "GET", "/v1/jobs/job", valid, 409, false},
		{"server-error", "POST", "/v1/jobs", valid, 500, false},
		{"truncated", "POST", "/v1/jobs", valid[:len(valid)-1], 409, false},
		{"trailing-json", "POST", "/v1/jobs", valid + "{}", 409, false},
		{"oversize", "POST", "/v1/jobs", valid + strings.Repeat(" ", 2*1024*1024), 409, false},
		{"wrong-id", "POST", "/v1/jobs", strings.Replace(valid, request.RequestID, "another", 1), 409, false},
		{"wrong-digest", "POST", "/v1/jobs", strings.Replace(valid, model.Hash(request), "another", 1), 409, false},
		{"unknown-code", "POST", "/v1/jobs", strings.Replace(valid, rejection.Code, "future_code", 1), 409, false},
		{"legacy", "POST", "/v1/jobs", `{"error":"not ready"}`, 409, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			server.TLS = &tls.Config{Certificates: []tls.Certificate{m.TLS}, MinVersion: tls.VersionTLS13}
			server.StartTLS()
			defer server.Close()
			p := model.Peer{ID: m.Config.Get().NodeID, Certificate: m.Certificate, Endpoint: server.URL}
			err := Call(context.Background(), p, tc.method, tc.path, request, nil)
			var refusal *model.TaskRejection
			if err == nil || errors.As(err, &refusal) != tc.definite {
				t.Fatal("incorrect acceptance proof", err)
			}
		})
	}
}
