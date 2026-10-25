package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

func ID() string             { return rand.Text() }
func Now() string            { return time.Now().UTC().Format(time.RFC3339Nano) }
func Digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func Hash(v any) string      { b, _ := json.Marshal(v); return Digest(b) }

type Request struct {
	RequestID      string `json:"request_id"`
	ProjectID      string `json:"project_id"`
	Goal           string `json:"goal"`
	StartBefore    string `json:"start_before"`
	Mode           string `json:"mode"`
	Target         string `json:"target,omitempty"`
	TargetExplicit bool   `json:"target_explicit,omitempty"`
}

type Job struct {
	ID           string       `json:"id"`
	Origin       string       `json:"origin"`
	Owner        string       `json:"owner"`
	Request      Request      `json:"request"`
	Digest       string       `json:"digest"`
	State        string       `json:"state"`
	Attempt      string       `json:"attempt,omitempty"`
	Created      string       `json:"created"`
	Updated      string       `json:"updated"`
	Seq          int64        `json:"seq"`
	Result       string       `json:"result,omitempty"`
	Error        string       `json:"error,omitempty"`
	Execution    string       `json:"execution_outcome"`
	Verification string       `json:"verification_outcome"`
	Acceptance   string       `json:"user_acceptance"`
	Artifacts    []Artifact   `json:"artifacts,omitempty"`
	Interaction  *Interaction `json:"interaction,omitempty"`
	RemoteID     string       `json:"remote_id,omitempty"`
	RemoteOwner  string       `json:"remote_owner,omitempty"`
}

func Terminal(state string) bool {
	return state == "succeeded" || state == "completed_unverified" || state == "failed" || state == "cancelled"
}

type Event struct {
	Seq     int64  `json:"seq"`
	JobID   string `json:"job_id"`
	Attempt string `json:"attempt,omitempty"`
	Type    string `json:"type"`
	Time    string `json:"time"`
	Data    any    `json:"data"`
}
type Artifact struct {
	ID        string `json:"id"`
	Operation string `json:"operation"`
	Path      string `json:"path"`
	Size      int    `json:"size"`
	SHA256    string `json:"sha256"`
	Preview   string `json:"preview"`
}
type Interaction struct {
	ID         string `json:"id"`
	Attempt    string `json:"attempt"`
	NativeID   string `json:"native_id"`
	Kind       string `json:"kind"`
	Question   string `json:"question"`
	Expires    string `json:"expires"`
	State      string `json:"state"`
	Answer     string `json:"answer,omitempty"`
	AnswerID   string `json:"answer_id,omitempty"`
	AnsweredBy string `json:"answered_by,omitempty"`
}
type Operation struct {
	ID       string    `json:"id"`
	JobID    string    `json:"job_id"`
	Attempt  string    `json:"attempt"`
	Digest   string    `json:"digest"`
	State    string    `json:"state"`
	Artifact *Artifact `json:"artifact,omitempty"`
}
type Peer struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Endpoint      string   `json:"endpoint"`
	Certificate   string   `json:"certificate"`
	OutboundToken string   `json:"outbound_token,omitempty"`
	InboundHash   string   `json:"inbound_hash,omitempty"`
	Projects      []string `json:"projects"`
	Revoked       bool     `json:"revoked"`
}
type Outbox struct {
	ID      string  `json:"id"`
	PeerID  string  `json:"peer_id"`
	Request Request `json:"request"`
	State   string  `json:"state"`
	Job     *Job    `json:"job,omitempty"`
	Error   string  `json:"error,omitempty"`
	Updated string  `json:"updated"`
	Cancel  bool    `json:"cancel"`
}
