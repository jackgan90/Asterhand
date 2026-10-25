package peer

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/network"
	"github.com/jackgan90/Asterhand/internal/store"
)

type Manager struct {
	Config      *config.Manager
	Store       *store.Store
	Certificate string
	TLS         tls.Certificate
}
type Invitation struct {
	NodeID      string   `json:"node_id"`
	Name        string   `json:"name"`
	Endpoint    string   `json:"endpoint"`
	Certificate string   `json:"certificate"`
	Code        string   `json:"code"`
	Expires     string   `json:"expires"`
	Projects    []string `json:"projects"`
}
type invitationRecord struct {
	Invitation  Invitation  `json:"invitation"`
	UsedBy      string      `json:"used_by,omitempty"`
	RequestHash string      `json:"request_hash,omitempty"`
	Response    *model.Peer `json:"response,omitempty"`
}
type PairRequest struct {
	Code string     `json:"code"`
	Node model.Peer `json:"node"`
}

func Open(c *config.Manager, s *store.Store) (*Manager, error) {
	certPath, keyPath := filepath.Join(c.Home, "node.crt"), filepath.Join(c.Home, "node.key")
	if _, err := os.Stat(certPath); errors.Is(err, os.ErrNotExist) {
		if _, keyErr := os.Stat(keyPath); keyErr == nil {
			return nil, errors.New("节点证书缺失但私钥存在，请恢复备份")
		}
		if err = createCertificate(c.Get().NodeID, certPath, keyPath); err != nil {
			return nil, err
		}
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(certPath)
	return &Manager{Config: c, Store: s, Certificate: string(b), TLS: pair}, err
}
func createCertificate(id, certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	t := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: id}, DNSNames: []string{id}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(5, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, t, t, &key.PublicKey, key)
	if err != nil {
		return err
	}
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err = config.AtomicFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}), 0600); err != nil {
		return err
	}
	return config.AtomicFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
}
func endpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("节点地址必须是 https://主机:端口")
	}
	return nil
}
func (m *Manager) Invite() (string, error) {
	c := m.Config.Get()
	if err := endpoint(c.Endpoint); err != nil {
		return "", err
	}
	projects := []string{}
	for _, p := range c.Projects {
		projects = append(projects, p.ID)
	}
	i := Invitation{NodeID: c.NodeID, Name: c.Name, Endpoint: c.Endpoint, Certificate: m.Certificate, Code: model.ID(), Expires: time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339Nano), Projects: projects}
	stored := i
	stored.Code = ""
	if err := m.Store.Put("invite", model.Digest([]byte(i.Code)), invitationRecord{Invitation: stored}); err != nil {
		return "", err
	}
	b, err := json.Marshal(i)
	return base64.RawURLEncoding.EncodeToString(b), err
}
func (m *Manager) AcceptPair(r PairRequest) (model.Peer, error) {
	var response model.Peer
	replayed := false
	if r.Node.ID == "" || r.Node.ID == m.Config.Get().NodeID || len(r.Node.OutboundToken) < 20 {
		return response, errors.New("无效的节点身份")
	}
	if err := endpoint(r.Node.Endpoint); err != nil {
		return response, err
	}
	if _, err := client(r.Node); err != nil {
		return response, err
	}
	err := m.Store.Modify("invite", model.Digest([]byte(r.Code)), func(raw []byte) (any, error) {
		var record invitationRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, err
		}
		if record.UsedBy != "" {
			if record.UsedBy != r.Node.ID || record.RequestHash != model.Hash(r.Node) {
				return nil, errors.New("配对码已使用")
			}
			replayed = true
			response = *record.Response
			var err error
			response.OutboundToken, err = config.Unseal(response.OutboundToken)
			return record, err
		}
		expiry, _ := time.Parse(time.RFC3339Nano, record.Invitation.Expires)
		if time.Now().After(expiry) {
			return nil, errors.New("配对码已过期")
		}
		c := m.Config.Get()
		token := model.ID()
		response = model.Peer{ID: c.NodeID, Name: c.Name, Endpoint: c.Endpoint, Certificate: m.Certificate, OutboundToken: token, Projects: record.Invitation.Projects}
		stored := response
		var err error
		stored.OutboundToken, err = config.Seal(stored.OutboundToken)
		if err != nil {
			return nil, err
		}
		record.UsedBy = r.Node.ID
		record.RequestHash = model.Hash(r.Node)
		record.Response = &stored
		return record, nil
	})
	if err != nil {
		return response, err
	}
	var existing model.Peer
	if err = m.Store.Get("peer", r.Node.ID, &existing); err == nil && existing.Revoked && replayed {
		return model.Peer{}, errors.New("此节点已撤销，请创建新的配对邀请")
	}
	p := r.Node
	p.InboundHash = model.Digest([]byte(response.OutboundToken))
	p.Projects = response.Projects
	p.Revoked = false
	return response, m.Save(p)
}
func (m *Manager) Pair(ctx context.Context, code string) (model.Peer, error) {
	var i Invitation
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(code))
	if err != nil {
		return model.Peer{}, errors.New("配对码格式无效")
	}
	if err = json.Unmarshal(b, &i); err != nil {
		return model.Peer{}, err
	}
	c := m.Config.Get()
	if i.NodeID == c.NodeID {
		return model.Peer{}, errors.New("请选择另一台节点")
	}
	if err = endpoint(c.Endpoint); err != nil {
		return model.Peer{}, errors.New("请先设置本机可达的 HTTPS 节点地址")
	}
	if !network.LoopbackEndpoint(i.Endpoint) && (network.LoopbackEndpoint(c.Endpoint) || network.LoopbackListener(c.PeerListen)) {
		return model.Peer{}, errors.New("本机节点仍使用回环地址。请设置 Tailscale 节点与监听地址，并重启后再配对")
	}
	target := model.Peer{ID: i.NodeID, Endpoint: i.Endpoint, Certificate: i.Certificate}
	// Persist the same handshake before network I/O so response loss is retryable.
	var own model.Peer
	key := model.Digest([]byte(i.Code))
	err = m.Store.Get("pair-pending", key, &own)
	if errors.Is(err, store.ErrNotFound) {
		own = model.Peer{ID: c.NodeID, Name: c.Name, Endpoint: c.Endpoint, Certificate: m.Certificate, OutboundToken: model.ID()}
		stored := own
		stored.OutboundToken, err = config.Seal(own.OutboundToken)
		if err == nil {
			err = m.Store.Put("pair-pending", key, stored)
		}
	} else if err == nil {
		own.OutboundToken, err = config.Unseal(own.OutboundToken)
	}
	if err != nil {
		return target, err
	}
	var response model.Peer
	if err = Call(ctx, target, "POST", "/v1/pair", PairRequest{Code: i.Code, Node: own}, &response); err != nil {
		return target, err
	}
	if response.ID != i.NodeID || response.Certificate != i.Certificate {
		return target, errors.New("配对目标身份与邀请不符")
	}
	response.InboundHash = model.Digest([]byte(own.OutboundToken))
	response.Projects = nil
	for _, project := range c.Projects {
		response.Projects = append(response.Projects, project.ID)
	}
	return response, m.Save(response)
}
func (m *Manager) Save(p model.Peer) error {
	var err error
	p.OutboundToken, err = config.Seal(p.OutboundToken)
	if err != nil {
		return err
	}
	return m.Store.Put("peer", p.ID, p)
}
func (m *Manager) Get(id string) (model.Peer, error) {
	var p model.Peer
	err := m.Store.Get("peer", id, &p)
	if err == nil {
		p.OutboundToken, err = config.Unseal(p.OutboundToken)
	}
	return p, err
}
func (m *Manager) List() ([]model.Peer, error) {
	var peers []model.Peer
	err := m.Store.List("peer", &peers)
	if err != nil {
		return nil, err
	}
	for i := range peers {
		peers[i].OutboundToken, err = config.Unseal(peers[i].OutboundToken)
		if err != nil {
			return nil, err
		}
	}
	return peers, nil
}
func client(p model.Peer) (*http.Client, error) {
	if err := endpoint(p.Endpoint); err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(p.Certificate)) {
		return nil, errors.New("无效的节点证书")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: p.ID}, MaxIdleConns: 2, ResponseHeaderTimeout: 20 * time.Second}
	return &http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("节点协议禁止重定向") }}, nil
}
func Call(ctx context.Context, p model.Peer, method, path string, input, output any) error {
	c, err := client(p)
	if err != nil {
		return err
	}
	defer c.CloseIdleConnections()
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.Endpoint, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.OutboundToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.OutboundToken)
	}
	res, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("连接 %s 失败: %w", p.Endpoint, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 2*1024*1024))
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		json.Unmarshal(b, &failure)
		if failure.Error == "" {
			failure.Error = res.Status
		}
		return errors.New(failure.Error)
	}
	if output != nil {
		return json.Unmarshal(b, output)
	}
	return nil
}
func (m *Manager) Authenticate(token string) (model.Peer, error) {
	var peers []model.Peer
	if err := m.Store.List("peer", &peers); err != nil {
		return model.Peer{}, err
	}
	hash := model.Digest([]byte(token))
	for _, p := range peers {
		if !p.Revoked && p.InboundHash == hash {
			return p, nil
		}
	}
	return model.Peer{}, errors.New("peer 凭据无效或已撤销")
}
