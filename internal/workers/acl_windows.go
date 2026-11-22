//go:build windows

package workers

import (
	"fmt"
	"os/user"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	aceTypeAccessAllowed = 0
	aceFlagInheritOnly   = 0x08
)

type winACLHeader struct {
	AclRevision byte
	Sbz1        byte
	AclSize     uint16
	AceCount    uint16
	Sbz2        uint16
}

type aceHeader struct {
	AceType  byte
	AceFlags byte
	AceSize  uint16
}

type accessAllowedAce struct {
	Header   aceHeader
	Mask     uint32
	SidStart uint32
}

// checkDeepSeekWorkspaceACL performs a strictly read-only Win32 DACL inspection
// to detect whether the current non-elevated user has explicit WRITE_DAC rights
// required by DeepSeek Harness's Windows pwsh sandbox (SetNamedSecurityInfoW).
func checkDeepSeekWorkspaceACL(workspaceRoot string) []string {
	has, known := dirGrantsNonAdminWriteDAC(workspaceRoot)
	if !known || has {
		return nil
	}
	userName := "%USERNAME%"
	if u, err := user.Current(); err == nil && u.Username != "" {
		if idx := strings.LastIndex(u.Username, `\`); idx >= 0 && idx+1 < len(u.Username) {
			userName = u.Username[idx+1:]
		} else {
			userName = u.Username
		}
	}
	return []string{
		fmt.Sprintf(
			`Workspace directory lacks an explicit WRITE_DAC (FullControl) ACE for the current non-elevated user; DeepSeek Harness pwsh sandbox may fail (built-in file read/write/edit tools still work). To enable pwsh, run once: icacls "%s" /grant "%s:(OI)(CI)F"`,
			workspaceRoot, userName,
		),
	}
}

func dirGrantsNonAdminWriteDAC(path string) (hasWriteDAC bool, known bool) {
	sd, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil || sd == nil {
		return false, false
	}
	dacl, defaulted, err := sd.DACL()
	if err != nil || dacl == nil {
		return err == nil && !defaulted, err == nil
	}
	tokUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || tokUser == nil || tokUser.User.Sid == nil {
		return false, false
	}
	userSID := tokUser.User.Sid
	authUsersSID, _ := windows.CreateWellKnownSid(windows.WinAuthenticatedUserSid)
	worldSID, _ := windows.CreateWellKnownSid(windows.WinWorldSid)

	base := unsafe.Pointer(dacl)
	aclHdr := (*winACLHeader)(base)
	aclSize := uintptr(aclHdr.AclSize)
	offset := unsafe.Sizeof(winACLHeader{})
	for i := uint16(0); i < aclHdr.AceCount && offset+unsafe.Sizeof(aceHeader{}) <= aclSize; i++ {
		hdr := (*aceHeader)(unsafe.Add(base, offset))
		if hdr.AceSize == 0 || offset+uintptr(hdr.AceSize) > aclSize {
			break
		}
		if hdr.AceType == aceTypeAccessAllowed && (hdr.AceFlags&aceFlagInheritOnly) == 0 && uintptr(hdr.AceSize) >= unsafe.Sizeof(accessAllowedAce{}) {
			ace := (*accessAllowedAce)(unsafe.Add(base, offset))
			if (ace.Mask & windows.WRITE_DAC) != 0 {
				aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
				if windows.EqualSid(aceSID, userSID) ||
					(authUsersSID != nil && windows.EqualSid(aceSID, authUsersSID)) ||
					(worldSID != nil && windows.EqualSid(aceSID, worldSID)) {
					return true, true
				}
			}
		}
		offset += uintptr(hdr.AceSize)
	}
	return false, true
}
