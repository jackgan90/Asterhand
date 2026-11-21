package automation

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

type Server struct {
	Registry *Registry
	Access   *Access
	server   *http.Server
	Handler  http.Handler
}

func PipeName(home, node string) string {
	hash := sha256.Sum256([]byte(strings.ToLower(home) + "\x00" + node))
	return `\\.\pipe\asterhand-automation-` + hex.EncodeToString(hash[:16])
}

func (s *Server) Start(ctx context.Context, pipe string, certificate tls.Certificate) error {
	listener, err := listenLocal(pipe)
	if err != nil {
		return err
	}
	s.Registry.Context = ctx
	handler := s.Handler
	if handler == nil {
		handler = s
	}
	s.server = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	go func() {
		_ = s.server.Serve(tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13}))
	}()
	return nil
}

func (s *Server) Close(ctx context.Context) error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if req.Method == "GET" && req.URL.Path == "/v1/definitions" && req.URL.RawQuery == "" && req.Header.Get("Origin") == "" {
		token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		p, err := s.Access.Authenticate(token)
		if err != nil {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(Response{Version: APIVersion, Error: PublicError(err)})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"api_version":     APIVersion,
			"instance_id":     s.Registry.Instance,
			"policy_revision": p.PolicyRevision,
			"definitions":     s.Registry.Definitions(p),
		})
		return
	}
	if req.Method != "POST" || req.URL.Path != "/v1/call" || req.URL.RawQuery != "" || req.Header.Get("Origin") != "" || req.Header.Get("Content-Type") != "application/json" {
		http.Error(w, `{"error":{"code":"invalid_request"}}`, http.StatusBadRequest)
		return
	}
	token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	if _, err := s.Access.Authenticate(token); err != nil {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(Response{Version: APIVersion, Error: PublicError(err)})
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, req.Body, MaxRequest))
	if err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	request, err := Decode[Request](b)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(Response{Version: APIVersion, Error: PublicError(err)})
		return
	}
	_ = json.NewEncoder(w).Encode(s.Registry.Dispatch(req.Context(), token, request))
}

func pinnedTLS(profile Profile) (*tls.Config, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(profile.Certificate)) {
		return nil, Fail("configuration", "The granted profile has an invalid server certificate")
	}
	return &tls.Config{RootCAs: roots, ServerName: profile.Node, MinVersion: tls.VersionTLS13}, nil
}
