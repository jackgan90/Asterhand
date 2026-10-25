package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/peer"
)

func TestPeerRenamePropagatesWithoutChangingTrust(t *testing.T) {
	a, b := newNode(t, &fakeExecutor{}), newNode(t, &fakeExecutor{})
	pairNodes(t, a, b)
	id := a.cfg.Get().NodeID
	before, err := b.app.Peers.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	response := saveNodeSettings(t, a, func(v map[string]any) { v["name"] = "home renamed" })
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var after model.Peer
	await(t, "metadata notification", func() bool { after, err = b.app.Peers.Get(id); return err == nil && after.Name == "home renamed" })
	before.Name = after.Name
	if !reflect.DeepEqual(before, after) {
		t.Fatal("metadata changed trust or permissions")
	}
	if !a.cfg.Get().ModelVerified {
		t.Fatal("rename invalidated model")
	}
}

func TestPeerAddressNotificationRepairsOneWayConnection(t *testing.T) {
	a, b := newNode(t, &fakeExecutor{}), newNode(t, &fakeExecutor{})
	pairNodes(t, a, b)
	id := a.cfg.Get().NodeID
	p, err := b.app.Peers.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	p.Endpoint = "https://127.0.0.1:1"
	p.Name = "old name"
	if err = b.app.Peers.Save(p); err != nil {
		t.Fatal(err)
	}
	// Restart A on another port, with the same node identity and credentials.
	a.close()
	a.start(t, &fakeExecutor{})
	c := a.cfg.Get()
	c.Name = "new name"
	if err = a.cfg.Save(c); err != nil {
		t.Fatal(err)
	}
	a.app.requestPeerSync()
	await(t, "reverse address repaired", func() bool {
		p, err = b.app.Peers.Get(id)
		return err == nil && p.Endpoint == c.Endpoint && p.Name == c.Name
	})
	var info map[string]any
	if err = peer.Call(context.Background(), p, "GET", "/v1/info", nil, &info); err != nil || info["node_id"] != id {
		t.Fatal("B still cannot read A", err)
	}
}

func TestMetadataRejectsIdentityCertificateAndUnreachableChanges(t *testing.T) {
	a, b, c := newNode(t, &fakeExecutor{}), newNode(t, &fakeExecutor{}), newNode(t, &fakeExecutor{})
	p := pairNodes(t, a, b)
	before, err := b.app.Peers.Get(a.cfg.Get().NodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []peerMetadata{
		{NodeID: c.cfg.Get().NodeID, Endpoint: a.cfg.Get().Endpoint},
		{NodeID: a.cfg.Get().NodeID, Endpoint: c.cfg.Get().Endpoint},
		{NodeID: a.cfg.Get().NodeID, Endpoint: "https://127.0.0.1:1"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := peer.Call(ctx, p, "POST", "/v1/metadata", input, nil)
		cancel()
		if err == nil {
			t.Fatal("invalid metadata accepted", input)
		}
		after, err := b.app.Peers.Get(before.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("rejected update changed peer", err)
		}
	}
	before.Revoked = true
	if err = b.app.Peers.Save(before); err != nil {
		t.Fatal(err)
	}
	if err = peer.Call(context.Background(), p, "POST", "/v1/metadata", peerMetadata{NodeID: a.cfg.Get().NodeID, Endpoint: a.cfg.Get().Endpoint}, nil); err == nil {
		t.Fatal("revoked peer updated metadata")
	}
}

func TestPeerInfoRefreshUpdatesNameAndRejectsWrongNodeID(t *testing.T) {
	a, b := newNode(t, &fakeExecutor{}), newNode(t, &fakeExecutor{})
	p := pairNodes(t, a, b)
	c := b.cfg.Get()
	c.Name = "working renamed"
	if err := b.cfg.Save(c); err != nil {
		t.Fatal(err)
	}
	info, err := a.app.Peers.Refresh(context.Background(), p.ID, "")
	if err != nil || info["name"] != c.Name {
		t.Fatal(info, err)
	}
	stored, err := a.app.Peers.Get(p.ID)
	if err != nil || stored.Name != c.Name {
		t.Fatal("name not persisted", err)
	}
	// The response ID must match even when the TLS certificate remains trusted.
	c.NodeID = model.ID()
	if err = b.cfg.Save(c); err != nil {
		t.Fatal(err)
	}
	if _, err = a.app.Peers.Refresh(context.Background(), p.ID, ""); err == nil {
		t.Fatal("wrong response node ID accepted")
	}
}

func TestRemotePairingRejectsLocalOnlyConfiguration(t *testing.T) {
	a, b := newNode(t, &fakeExecutor{}), newNode(t, &fakeExecutor{})
	code, err := b.app.Peers.Invite()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil {
		t.Fatal(err)
	}
	var invitation peer.Invitation
	if err = json.Unmarshal(decoded, &invitation); err != nil {
		t.Fatal(err)
	}
	invitation.Endpoint = "https://100.102.229.69:17433"
	encoded, _ := json.Marshal(invitation)
	if _, err = a.app.Peers.Pair(context.Background(), base64.RawURLEncoding.EncodeToString(encoded)); err == nil {
		t.Fatal("loopback configuration accepted for remote pairing")
	}
}
