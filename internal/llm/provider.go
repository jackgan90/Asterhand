// Package llm describes model configuration. The agent runtime owns the LLM loop.
package llm

import (
	"slices"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/packaging"
)

type Selection struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	CredentialRef string `json:"credential_ref"`
	Endpoint      string `json:"endpoint,omitempty"`
}

type Descriptor struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Models         []string `json:"models"`
	CustomEndpoint bool     `json:"custom_endpoint"`
}

type Provider interface {
	Describe() Descriptor
	Validate(Selection) error
}

type DeepSeek struct{}

func (DeepSeek) Describe() Descriptor {
	return Descriptor{ID: "deepseek", Name: "DeepSeek", Models: []string{"deepseek/deepseek-flash", "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4-pro"}}
}
func (d DeepSeek) Validate(s Selection) error {
	if s.Provider != d.Describe().ID || !slices.Contains(d.Describe().Models, s.Model) {
		return i18n.ErrArgs("err.llm.model_unsupported", map[string]string{"model": s.Model})
	}
	if s.Endpoint != "" {
		return i18n.Err("err.llm.custom_endpoint_unsupported")
	}
	return nil
}
func Resolve(id string) (Provider, error) {
	switch id {
	case "deepseek":
		return DeepSeek{}, nil
	default:
		return nil, i18n.ErrArgs("err.llm.provider_missing", map[string]string{"id": id})
	}
}
func Available() []Descriptor { return []Descriptor{DeepSeek{}.Describe()} }
func Default() Selection {
	d := packaging.Dependencies().DefaultTarget()
	return Selection{Provider: d.Provider, Model: d.Model, CredentialRef: d.CredentialRef, Endpoint: d.Endpoint}
}
func Validate(s Selection) error {
	p, err := Resolve(s.Provider)
	if err != nil {
		return err
	}
	return p.Validate(s)
}
