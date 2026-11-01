package config

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/llm"
	"github.com/jackgan90/Asterhand/packaging"
)

var credentialID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// normalize is the only migration from the original flat model configuration.
// Credentials stay at the original path so migration never duplicates a secret.
func normalize(c Config) Config {
	if c.AgentRuntime == "" {
		c.AgentRuntime = packaging.Dependencies().DefaultTarget().Runtime
	}
	if c.LLM.Provider == "" && c.LLM.Model == "" {
		c.LLM = llm.Default()
		if c.Model != "" {
			c.LLM.Model = c.Model
		}
	}
	c.Model = ""
	if len(c.Projects) > 0 {
		c.Projects = append([]Project(nil), c.Projects...)
		for i := range c.Projects {
			if c.Projects[i].ID == "workspace" && !c.Projects[i].NameCustom {
				if c.Projects[i].Name == "" {
					c.Projects[i].Name = legacyDefaultWorkspaceName
				} else if c.Projects[i].Name != legacyDefaultWorkspaceName {
					c.Projects[i].NameCustom = true
				}
			}
		}
	}
	return c
}
func ValidateSelection(c Config) error {
	if !credentialID.MatchString(c.LLM.CredentialRef) {
		return i18n.Err("err.config.invalid_credential_ref")
	}
	return llm.Validate(c.LLM)
}
func (m *Manager) SetCredential(ref, key string) error {
	if !credentialID.MatchString(ref) {
		return i18n.Err("err.config.invalid_credential_ref")
	}
	path := filepath.Join(m.Home, ref+".secret")
	if key == "" {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	b, err := protect([]byte(key))
	if err != nil {
		return err
	}
	return AtomicFile(path, b, 0600)
}
func (m *Manager) Credential(ref string) (string, error) {
	if !credentialID.MatchString(ref) {
		return "", i18n.Err("err.config.invalid_credential_ref")
	}
	b, err := os.ReadFile(filepath.Join(m.Home, ref+".secret"))
	if err != nil {
		return "", err
	}
	b, err = unprotect(b)
	return string(b), err
}
