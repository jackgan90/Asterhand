package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/network"
	"github.com/jackgan90/Asterhand/internal/peer"
)

const (
	peerProbeTimeout        = 10 * time.Second
	peerMetadataSendTimeout = 15 * time.Second
)

type peerMetadata struct {
	NodeID   string `json:"node_id"`
	Endpoint string `json:"endpoint"`
}

func (a *App) requestPeerSync() {
	select {
	case a.peerSync <- struct{}{}:
	default:
	}
}

func (a *App) peerSyncLoop(ctx context.Context) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		a.syncPeers(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-a.peerSync:
		}
	}
}

func (a *App) syncPeers(ctx context.Context) {
	c := a.Config.Get()
	if c.Endpoint == "" || !network.ListenerMatches(c.PeerListen, a.peerAddress) {
		return
	}
	peers, err := a.Peers.List()
	if err != nil {
		return
	}
	for _, p := range peers {
		if ctx.Err() != nil {
			return
		}
		if p.Revoked {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, peerMetadataSendTimeout)
		// Unsupported older peers keep their current state; the next cycle retries.
		_ = peer.Call(callCtx, p, "POST", "/v1/metadata", peerMetadata{NodeID: c.NodeID, Endpoint: c.Endpoint}, nil)
		cancel()
	}
}

func (a *App) receiveMetadata(w http.ResponseWriter, r *http.Request, p model.Peer) {
	if r.Method != "POST" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input peerMetadata
	if err := read(w, r, &input); err != nil {
		fail(w, 400, err)
		return
	}
	if input.NodeID != p.ID || input.Endpoint == "" || len(input.Endpoint) > 2048 {
		fail(w, 403, errors.New("元数据只能更新已认证的原节点"))
		return
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() && network.LoopbackEndpoint(input.Endpoint) {
		fail(w, 400, errors.New("远端节点不能公布本机回环地址"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), peerProbeTimeout)
	defer cancel()
	if _, err := a.Peers.Refresh(ctx, p.ID, input.Endpoint); err != nil {
		fail(w, 409, err)
		return
	}
	send(w, 200, map[string]bool{"updated": true})
}
