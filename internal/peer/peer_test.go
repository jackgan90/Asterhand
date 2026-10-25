package peer

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/store"
)

func manager(t *testing.T) *Manager {
	t.Helper()
	c, err := config.Open(filepath.Join(t.TempDir(), "home"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := c.Get()
	cfg.Endpoint = "https://127.0.0.1:17433"
	cfg.Projects = []config.Project{{ID: "workspace"}}
	if err = c.Save(cfg); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(c.Home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	m, err := Open(c, s)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func invitation(t *testing.T, m *Manager) Invitation {
	t.Helper()
	encoded, err := m.Invite()
	if err != nil {
		t.Fatal(err)
	}
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var i Invitation
	if err = json.Unmarshal(b, &i); err != nil {
		t.Fatal(err)
	}
	return i
}
func TestHandshakeRetryBindingRevocationAndSealedSecrets(t *testing.T) {
	a, b := manager(t), manager(t)
	i := invitation(t, b)
	own := model.Peer{ID: a.Config.Get().NodeID, Name: "A", Endpoint: a.Config.Get().Endpoint, Certificate: a.Certificate, OutboundToken: model.ID()}
	request := PairRequest{Code: i.Code, Node: own}
	first, err := b.AcceptPair(request)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := b.AcceptPair(request)
	if err != nil || retry.OutboundToken != first.OutboundToken {
		t.Fatal("lost-response retry changed credentials", err)
	}
	changed := request
	changed.Node.OutboundToken = model.ID()
	if _, err = b.AcceptPair(changed); err == nil {
		t.Fatal("replayed identity changed return credential")
	}
	var raw model.Peer
	if err = b.Store.Get("peer", own.ID, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.OutboundToken == own.OutboundToken || raw.InboundHash == first.OutboundToken {
		t.Fatal("credential stored in plaintext")
	}
	opened, err := b.Get(own.ID)
	if err != nil || opened.OutboundToken != own.OutboundToken {
		t.Fatal("sealed credential cannot be recovered", err)
	}
	raw.Revoked = true
	if err = b.Store.Put("peer", own.ID, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = b.AcceptPair(request); err == nil {
		t.Fatal("old invitation restored revoked access")
	}
	request.Code = invitation(t, b).Code
	if _, err = b.AcceptPair(request); err != nil {
		t.Fatal("fresh invitation could not explicitly regrant", err)
	}
}
func TestExpiredInvitationCannotPair(t *testing.T) {
	a, b := manager(t), manager(t)
	i := invitation(t, b)
	key := model.Digest([]byte(i.Code))
	var record invitationRecord
	if err := b.Store.Get("invite", key, &record); err != nil {
		t.Fatal(err)
	}
	record.Invitation.Expires = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
	b.Store.Put("invite", key, record)
	_, err := b.AcceptPair(PairRequest{Code: i.Code, Node: model.Peer{ID: a.Config.Get().NodeID, Endpoint: a.Config.Get().Endpoint, Certificate: a.Certificate, OutboundToken: model.ID()}})
	if err == nil {
		t.Fatal("expired invitation accepted")
	}
}
