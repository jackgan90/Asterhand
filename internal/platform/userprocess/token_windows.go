//go:build windows

package userprocess

import (
	"runtime"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"golang.org/x/sys/windows"
)

func tokenInfo(token windows.Token, class uint32) ([]byte, error) {
	var size uint32
	_ = windows.GetTokenInformation(token, class, nil, 0, &size)
	if size == 0 || size > 1024*1024 {
		return nil, i18n.Err("err.quickinput.token_read_failed")
	}
	data := make([]byte, size)
	err := windows.GetTokenInformation(token, class, &data[0], size, &size)
	return data, err
}

func VerifyToken(token windows.Token) error {
	elevation, err := tokenInfo(token, windows.TokenElevation)
	if err != nil {
		return err
	}
	if *(*uint32)(unsafe.Pointer(&elevation[0])) != 0 {
		return i18n.Err("err.quickinput.token_unelevated_required")
	}
	label, err := tokenInfo(token, windows.TokenIntegrityLevel)
	if err != nil {
		return err
	}
	defer runtime.KeepAlive(label)
	il := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&label[0]))
	if il.Label.Sid.String() != "S-1-16-8192" {
		return i18n.Err("err.quickinput.token_medium_il")
	}
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	if user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		return i18n.Err("err.quickinput.system_unsupported")
	}
	groups, err := token.GetTokenGroups()
	if err != nil {
		return err
	}
	for _, group := range groups.AllGroups() {
		if group.Sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) && (group.Attributes&windows.SE_GROUP_ENABLED != 0 || group.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0) {
			return i18n.Err("err.quickinput.token_admin_enabled")
		}
	}
	return nil
}

// OrdinaryToken only changes the new token. No desktop, directory or
// machine policy ACL is altered. A non-elevated parent uses its own token.
func OrdinaryToken() (windows.Token, error) {
	var original windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_ADJUST_DEFAULT, &original); err != nil {
		return 0, err
	}
	defer original.Close()
	user, err := original.GetTokenUser()
	if err != nil {
		return 0, err
	}
	if user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		return 0, i18n.Err("err.quickinput.system_unsupported")
	}
	if !original.IsElevated() {
		return 0, VerifyToken(original)
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return 0, err
	}
	disable := windows.SIDAndAttributes{Sid: admins}
	var token windows.Token
	const disableMaxPrivilegeAndLUA = 0x5
	ok, _, callErr := windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken").Call(uintptr(original), disableMaxPrivilegeAndLUA, 1, uintptr(unsafe.Pointer(&disable)), 0, 0, 0, 0, uintptr(unsafe.Pointer(&token)))
	runtime.KeepAlive(admins)
	if ok == 0 {
		return 0, i18n.Wrap("err.quickinput.restricted_token_failed", map[string]string{"err": callErr.Error()}, callErr)
	}
	if err = configureHostToken(token, user.User.Sid.String()); err != nil {
		token.Close()
		return 0, err
	}
	return token, nil
}

func configureHostToken(token windows.Token, userSID string) error {
	medium, err := windows.StringToSid("S-1-16-8192")
	if err != nil {
		return err
	}
	label := windows.Tokenmandatorylabel{Label: windows.SIDAndAttributes{Sid: medium, Attributes: windows.SE_GROUP_INTEGRITY}}
	err = windows.SetTokenInformation(token, windows.TokenIntegrityLevel, (*byte)(unsafe.Pointer(&label)), label.Size())
	runtime.KeepAlive(medium)
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:(A;;GA;;;SY)(A;;GA;;;" + userSID + ")")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err == nil {
		err = windows.SetTokenInformation(token, windows.TokenDefaultDacl, (*byte)(unsafe.Pointer(&acl)), uint32(unsafe.Sizeof(acl)))
	}
	runtime.KeepAlive(sd)
	if err != nil {
		return err
	}
	if err = VerifyToken(token); err != nil {
		return err
	}
	privileges, err := tokenInfo(token, windows.TokenPrivileges)
	if err != nil {
		return err
	}
	// DISABLE_MAX_PRIVILEGE retains only SeChangeNotifyPrivilege (LUID 23).
	for _, p := range (*windows.Tokenprivileges)(unsafe.Pointer(&privileges[0])).AllPrivileges() {
		if p.Luid.HighPart != 0 || p.Luid.LowPart != 23 {
			return i18n.Err("err.quickinput.token_extra_privs")
		}
	}
	return nil
}
