package localcommands

import (
	"bytes"
	"encoding/json"
)

// NotificationSetting is a default-on boolean with presence information. The
// zero value means omitted, so old catalogs default on and old edits preserve
// an explicit off value. Its wire representation is always a JSON boolean.
type NotificationSetting uint8

const (
	NotificationDefault NotificationSetting = iota
	NotificationOn
	NotificationOff
)

func (s NotificationSetting) Enabled() bool                { return s != NotificationOff }
func (s NotificationSetting) Present() bool                { return s != NotificationDefault }
func (s NotificationSetting) MarshalJSON() ([]byte, error) { return json.Marshal(s.Enabled()) }
func (s *NotificationSetting) UnmarshalJSON(b []byte) error {
	switch string(bytes.TrimSpace(b)) {
	case "true":
		*s = NotificationOn
	case "false":
		*s = NotificationOff
	default:
		return problem("invalid", "err.localcmd.invalid_notification_setting")
	}
	return nil
}
