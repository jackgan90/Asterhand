package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"sync"

	"github.com/jackgan90/Asterhand/internal/llm"
	"github.com/jackgan90/Asterhand/internal/model"
)

const Version = "0.1.0-alpha.1"

// SourceDigest is supplied by the candidate builder and binds live evidence to source.
var SourceDigest = "development"

type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Root string `json:"root"`
}
type Config struct {
	NodeID                 string                     `json:"node_id"`
	Name                   string                     `json:"name"`
	WebListen              string                     `json:"web_listen"`
	PeerListen             string                     `json:"peer_listen"`
	Endpoint               string                     `json:"endpoint"`
	Model                  string                     `json:"model,omitempty"` // Legacy read-only migration field.
	LLM                    llm.Selection              `json:"llm"`
	AgentRuntime           string                     `json:"agent_runtime"`
	Projects               []Project                  `json:"projects"`
	RuntimeOptions         map[string]json.RawMessage `json:"runtime_options,omitempty"`
	RuntimeOptionsExplicit map[string]bool            `json:"runtime_options_explicit,omitempty"`
	Configured             bool                       `json:"configured"`
	Profile                string                     `json:"profile,omitempty"`
	ModelVerified          bool                       `json:"model_verified"`
	VerifiedBundle         string                     `json:"verified_bundle,omitempty"`
	VerifiedExecution      string                     `json:"verified_execution,omitempty"`
	AcceptanceDelay        int                        `json:"acceptance_delay_seconds,omitempty"`
}
type Manager struct {
	mu   sync.RWMutex
	Home string
	cfg  Config
}

func DefaultHome() string {
	if p := os.Getenv("ASTERHAND_HOME"); p != "" {
		return p
	}
	p, err := os.UserConfigDir()
	if err != nil {
		return "asterhand-data"
	}
	return filepath.Join(p, "Asterhand")
}
func Open(home string) (*Manager, error) {
	abs, err := filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	if err := validateHome(abs); err != nil {
		return nil, err
	}
	if err := privateDirectory(abs); err != nil {
		return nil, err
	}
	m := &Manager{Home: abs}
	b, err := os.ReadFile(filepath.Join(abs, "config.json"))
	if err == nil {
		err = json.Unmarshal(b, &m.cfg)
		m.cfg = normalize(m.cfg)
		return m, err
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	m.cfg = Config{NodeID: model.ID(), Name: "我的节点", WebListen: "127.0.0.1:17432", PeerListen: "0.0.0.0:17433"}
	return m, m.Save(m.cfg)
}
func validateHome(path string) error {
	user, _ := os.UserHomeDir()
	if filepath.Dir(path) == path || path == user {
		return errors.New("节点数据目录必须是独立的应用目录")
	}
	if _, err := os.Stat(filepath.Join(path, "config.json")); err == nil {
		return nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "startup.txt", "error.txt", "process.pid", "startup.log", "error.log", "launcher.log", "service.lock", "launch-state.json":
		default:
			return errors.New("此目录已有其他内容，请选择空目录或已有 Asterhand 数据目录")
		}
	}
	return nil
}
func (m *Manager) Get() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c := m.cfg
	c.Projects = append([]Project(nil), c.Projects...)
	c.RuntimeOptions = cloneOptions(c.RuntimeOptions)
	c.RuntimeOptionsExplicit = maps.Clone(c.RuntimeOptionsExplicit)
	return c
}
func (m *Manager) Save(c Config) error {
	c = normalize(c)
	c.RuntimeOptions = cloneOptions(c.RuntimeOptions)
	c.RuntimeOptionsExplicit = maps.Clone(c.RuntimeOptionsExplicit)
	m.mu.Lock()
	defer m.mu.Unlock()
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err = AtomicFile(filepath.Join(m.Home, "config.json"), b, 0600); err != nil {
		return err
	}
	m.cfg = c
	return nil
}
func AtomicFile(path string, b []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".asterhand-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, path)
}
func (m *Manager) SetKey(key string) error { return m.SetCredential(m.Get().LLM.CredentialRef, key) }
func (m *Manager) Key() (string, error)    { return m.Credential(m.Get().LLM.CredentialRef) }
func Seal(secret string) (string, error) {
	if secret == "" {
		return "", nil
	}
	b, err := protect([]byte(secret))
	if err != nil {
		return "", err
	}
	return "sealed:" + base64.RawStdEncoding.EncodeToString(b), nil
}
func Unseal(secret string) (string, error) {
	if secret == "" {
		return "", nil
	}
	if len(secret) < 7 || secret[:7] != "sealed:" {
		return "", errors.New("凭据格式无效，请重新配对")
	}
	b, err := base64.RawStdEncoding.DecodeString(secret[7:])
	if err != nil {
		return "", err
	}
	b, err = unprotect(b)
	return string(b), err
}
