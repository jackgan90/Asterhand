package automation

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/jackgan90/Asterhand/internal/config"
)

const localActor = "asterhand-local-user"

func localSecret(login []byte) string {
	mac := hmac.New(sha256.New, login)
	mac.Write([]byte("Asterhand local automation v1"))
	return hex.EncodeToString(mac.Sum(nil))
}

func localPrincipal() Principal {
	return Principal{ID: localActor, Name: "Local user", Roles: []string{"read", "edit", "execute"},
		Scopes: map[string][]string{"commands": {"*"}, "projects": {"*"}, "conversations": {"*"}, "node": {"*"}}}
}

// The node publishes only its address and public certificate. The current
// user's existing login file supplies a separate local-channel credential.
func (a *Access) ConfigureLocal(profile Profile, login string) error {
	if len(login) < 16 {
		return Fail("configuration", "Local node login is unavailable")
	}
	profile.Version, profile.Actor = APIVersion, localActor
	b, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	if err = config.AtomicFile(filepath.Join(a.home, "automation-local.json"), b, 0600); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.localHash = sha256.Sum256([]byte(localSecret([]byte(login))))
	a.localReady = true
	return nil
}

func openLocalClient(home string) (*Client, error) {
	b, err := os.ReadFile(filepath.Join(home, "automation-local.json"))
	if err != nil {
		return nil, Fail("node_unavailable", "Start this node once to initialize its local connection")
	}
	var p Profile
	if len(b) > 64<<10 || json.Unmarshal(b, &p) != nil || p.Actor != localActor || p.Version != APIVersion || p.Pipe != PipeName(home, p.Node) {
		return nil, Fail("configuration", "Invalid local node connection")
	}
	login, err := os.ReadFile(filepath.Join(home, "web-login.token"))
	if err != nil || len(login) < 16 || len(login) > 256 {
		return nil, Fail("authorization_required", "Current user cannot read the node login")
	}
	c, err := newTokenClient(p, localSecret(login))
	if err == nil {
		c.home = home
	}
	return c, err
}
