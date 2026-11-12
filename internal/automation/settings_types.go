package automation

import "github.com/jackgan90/Asterhand/internal/quickinput"

type SettingsPatch struct {
	Settings string              `json:"settings" description:"node or desktop; one persisted object per atomic patch"`
	Revision string              `json:"expected_revision"`
	Name     *string             `json:"name,omitempty"`
	Model    *string             `json:"model,omitempty"`
	Enabled  *bool               `json:"enabled,omitempty"`
	Binding  *quickinput.Binding `json:"binding,omitempty"`
	Theme    *string             `json:"theme_id,omitempty"`
	Locale   *string             `json:"locale,omitempty"`
	Reset    bool                `json:"reset,omitempty"`
}

type ModelSelect struct {
	Model    string `json:"model"`
	Revision string `json:"expected_revision"`
}
type HotkeyCheck struct {
	Binding quickinput.Binding `json:"binding"`
}
