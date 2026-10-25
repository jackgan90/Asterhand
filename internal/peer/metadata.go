package peer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/store"
)

// Refresh verifies both identity and the advertised destination before replacing
// metadata. It never imports credentials, certificates or project permissions.
func (m *Manager) Refresh(ctx context.Context, id, address string) (map[string]any, error) {
	p, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if p.Revoked {
		return nil, errors.New("此 peer 已撤销")
	}
	candidate := p
	if address != "" {
		candidate.Endpoint = strings.TrimRight(address, "/")
	}
	info, err := observe(ctx, candidate)
	if err != nil {
		return nil, err
	}
	advertised := strings.TrimRight(info["endpoint"].(string), "/")
	if advertised != strings.TrimRight(candidate.Endpoint, "/") {
		candidate.Endpoint = advertised
		info, err = observe(ctx, candidate)
		if err != nil {
			return nil, err
		}
		if strings.TrimRight(info["endpoint"].(string), "/") != advertised {
			return nil, errors.New("节点地址在校验期间变化，请稍后重试")
		}
	}
	name := info["name"].(string)
	err = m.Store.Modify("peer", id, func(raw []byte) (any, error) {
		var current model.Peer
		if err := json.Unmarshal(raw, &current); err != nil {
			return nil, err
		}
		if current.Revoked || current.Certificate != p.Certificate || current.InboundHash != p.InboundHash {
			return nil, errors.New("配对身份或授权已变化，请重新检查节点")
		}
		if current.Name == name && current.Endpoint == advertised {
			return nil, store.ErrUnchanged
		}
		current.Name, current.Endpoint = name, advertised
		return current, nil
	})
	if errors.Is(err, store.ErrUnchanged) {
		err = nil
	}
	return info, err
}

func observe(ctx context.Context, p model.Peer) (map[string]any, error) {
	var info map[string]any
	if err := Call(ctx, p, "GET", "/v1/info", nil, &info); err != nil {
		return nil, err
	}
	name, nameOK := info["name"].(string)
	address, addressOK := info["endpoint"].(string)
	if info["node_id"] != p.ID || !nameOK || strings.TrimSpace(name) == "" || len(name) > 80 || !addressOK || len(address) > 2048 {
		return nil, errors.New("节点元数据与已配对身份不符")
	}
	if err := endpoint(address); err != nil {
		return nil, err
	}
	return info, nil
}
