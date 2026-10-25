// Package packaging embeds the same dependency catalog consumed by installers and builds.
package packaging

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
)

func Digest() [32]byte { return sha256.Sum256(catalogJSON) }

//go:embed dependencies.json
var catalogJSON []byte

type Component struct {
	ID      string `json:"id"`
	Manager string `json:"manager"`
	Version string `json:"version"`
	Package string `json:"package"`
}
type Catalog struct {
	Schema     int         `json:"schema"`
	Go         Component   `json:"go"`
	Node       Component   `json:"node"`
	Components []Component `json:"components"`
	Bundle     Bundle      `json:"bundle"`
	WebView2   struct {
		MinimumRuntimeVersion string `json:"minimum_runtime_version"`
	} `json:"webview2"`
}

type Target struct {
	ID            string `json:"id"`
	Runtime       string `json:"runtime"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	CredentialRef string `json:"credential_ref"`
	Endpoint      string `json:"endpoint,omitempty"`
}
type Bundle struct {
	DefaultTarget string   `json:"default_target"`
	Targets       []Target `json:"targets"`
}

func (c Catalog) Component(id string) (Component, error) {
	for _, component := range c.Components {
		if component.ID == id {
			return component, nil
		}
	}
	return Component{}, fmt.Errorf("安装包缺少组件: %s", id)
}
func (c Catalog) DefaultTarget() Target {
	for _, target := range c.Bundle.Targets {
		if target.ID == c.Bundle.DefaultTarget {
			return target
		}
	}
	panic("validated catalog has no default target")
}
func (c Catalog) Validate() error {
	if c.Schema != 2 || len(c.Components) == 0 || len(c.Bundle.Targets) == 0 {
		return fmt.Errorf("安装包描述缺少组件/目标或 schema 不支持")
	}
	ids, packages := map[string]bool{}, map[string]bool{}
	for _, component := range c.Components {
		if component.ID == "" || ids[component.ID] || component.Manager != "npm" || component.Package == "" || packages[component.Package] || component.Version == "" {
			return fmt.Errorf("安装包组件描述无效: %s", component.ID)
		}
		ids[component.ID], packages[component.Package] = true, true
	}
	ids = map[string]bool{}
	for _, target := range c.Bundle.Targets {
		if target.ID == "" || ids[target.ID] || target.Runtime == "" || target.Provider == "" || target.Model == "" || target.CredentialRef == "" {
			return fmt.Errorf("安装包目标描述无效: %s", target.ID)
		}
		ids[target.ID] = true
	}
	if !ids[c.Bundle.DefaultTarget] {
		return fmt.Errorf("安装包默认目标不存在")
	}
	return nil
}

func Dependencies() Catalog {
	var c Catalog
	if err := json.Unmarshal(catalogJSON, &c); err != nil {
		panic(err)
	}
	if err := c.Validate(); err != nil {
		panic(err)
	}
	return c
}
