package quickinput

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jackgan90/Asterhand/internal/model"
)

type Preferences struct {
	Schema   int     `json:"schema_version"`
	Revision string  `json:"revision"`
	Enabled  bool    `json:"enabled"`
	Binding  Binding `json:"binding"`
	Theme    string  `json:"theme_id"`
}

func Defaults() Preferences { return Preferences{1, model.ID(), true, DefaultBinding(), "star"} }

func (p Preferences) validated() (Preferences, error) {
	if p.Schema != 1 || p.Revision == "" || len(p.Revision) > 100 {
		return p, fmt.Errorf("桌面配置格式或版本无效")
	}
	if p.Theme != "star" && p.Theme != "frost" {
		return p, fmt.Errorf("请选择内置主题")
	}
	binding, err := p.Binding.Normalize()
	p.Binding = binding
	return p, err
}

type PreferencesStore struct{ path string }

func NewPreferencesStore(home string) *PreferencesStore {
	return &PreferencesStore{filepath.Join(home, "desktop-settings.json")}
}

func (s *PreferencesStore) Load() (Preferences, error) {
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		p := Defaults()
		return p, s.Save(p)
	}
	if err != nil {
		return Preferences{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 16*1024+1))
	if err != nil {
		return Preferences{}, err
	}
	if len(b) > 16*1024 {
		return Preferences{}, fmt.Errorf("桌面配置超过 16 KiB")
	}
	var p Preferences
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&p); err != nil {
		return p, fmt.Errorf("桌面配置损坏，请恢复默认或修复原文件")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return p, fmt.Errorf("桌面配置包含多余内容")
	}
	return p.validated()
}

func (s *PreferencesStore) Save(p Preferences) error {
	if _, err := p.validated(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".desktop-settings-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(b, '\n'))
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
	return replacePreferences(tmp, s.path)
}

func (s *PreferencesStore) PreserveInvalid() error {
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	backup, err := os.OpenFile(s.path+".recovery-"+model.ID(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(backup, f)
	if err == nil {
		err = backup.Sync()
	}
	closeErr := backup.Close()
	if err != nil {
		return err
	}
	return closeErr
}
