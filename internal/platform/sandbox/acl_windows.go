//go:build windows

package sandbox

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	modifyAccessMask       = 0x001301BF // Modify without WRITE_DAC or WRITE_OWNER
	restrictedMaxPriv      = 0x00000001 // DISABLE_MAX_PRIVILEGE
	restrictedLUAToken     = 0x00000004 // LUA_TOKEN
	restrictedWriteOnly    = 0x00000008 // WRITE_RESTRICTED
	aclRevision            = 2
	maxGrantDepth          = 64
	maxGrantVisitedEntries = 50000
)

var (
	advapi32                = windows.NewLazySystemDLL("advapi32.dll")
	procCreateRestrictedTok = advapi32.NewProc("CreateRestrictedToken")
	procInitializeAcl       = advapi32.NewProc("InitializeAcl")
)

// DeriveSandboxSID computes the deterministic S-1-9-<u1>-<u2>-<u3>-<u4>
// capability SID bound to a cleaned lowercase workspace root path.
func DeriveSandboxSID(workspaceRoot string) (*windows.SID, error) {
	clean := strings.ToLower(stripExtendedPrefix(filepath.Clean(workspaceRoot)))
	mac := hmac.New(sha256.New, []byte("asterhand-workspace-sandbox-v1"))
	mac.Write([]byte(clean))
	digest := mac.Sum(nil)
	sidStr := fmt.Sprintf("S-1-9-%d-%d-%d-%d",
		binary.LittleEndian.Uint32(digest[0:4]),
		binary.LittleEndian.Uint32(digest[4:8]),
		binary.LittleEndian.Uint32(digest[8:12]),
		binary.LittleEndian.Uint32(digest[12:16]),
	)
	return windows.StringToSid(sidStr)
}

// CreateWriteRestrictedToken derives a non-elevated WRITE_RESTRICTED primary token
// whose writable kernel identity is limited to sandboxSID (plus S-1-1-0 Everyone for
// standard runtime compatibility on \Device\Null and system DLLs).
func CreateWriteRestrictedToken(sandboxSID *windows.SID) (windows.Token, error) {
	if sandboxSID == nil {
		return 0, fail("invalid_request")
	}
	var base windows.Token
	err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_ADJUST_DEFAULT,
		&base,
	)
	if err != nil {
		return 0, err
	}
	defer base.Close()

	writeRestrictedCode, err := windows.StringToSid("S-1-5-33")
	if err != nil {
		return 0, err
	}
	everyoneSID, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		return 0, err
	}
	restrictSids := []windows.SIDAndAttributes{
		{Sid: sandboxSID},
		{Sid: writeRestrictedCode},
		{Sid: everyoneSID},
	}
	logonBuf, logonSids, err := readTokenLogonSIDs(base)
	if err != nil {
		return 0, err
	}
	for _, ls := range logonSids {
		restrictSids = append(restrictSids, windows.SIDAndAttributes{Sid: ls})
	}

	var restricted windows.Token
	ok, _, callErr := procCreateRestrictedTok.Call(
		uintptr(base),
		restrictedMaxPriv|restrictedLUAToken|restrictedWriteOnly,
		0, 0, 0, 0,
		uintptr(len(restrictSids)),
		uintptr(unsafe.Pointer(&restrictSids[0])),
		uintptr(unsafe.Pointer(&restricted)),
	)
	runtime.KeepAlive(sandboxSID)
	runtime.KeepAlive(writeRestrictedCode)
	runtime.KeepAlive(everyoneSID)
	runtime.KeepAlive(logonBuf)
	runtime.KeepAlive(restrictSids)
	if ok == 0 {
		return 0, callErr
	}
	if err := configureRestrictedToken(restricted, base, sandboxSID, logonSids); err != nil {
		_ = restricted.Close()
		return 0, err
	}
	return restricted, nil
}

func readTokenLogonSIDs(tok windows.Token) ([]byte, []*windows.SID, error) {
	var needed uint32
	_ = windows.GetTokenInformation(tok, windows.TokenLogonSid, nil, 0, &needed)
	if needed == 0 {
		return nil, nil, nil
	}
	buf := make([]byte, needed)
	if err := windows.GetTokenInformation(tok, windows.TokenLogonSid, &buf[0], needed, &needed); err != nil {
		return nil, nil, nil
	}
	groups := (*windows.Tokengroups)(unsafe.Pointer(&buf[0]))
	var sids []*windows.SID
	for _, g := range groups.AllGroups() {
		if g.Sid != nil {
			sids = append(sids, g.Sid)
		}
	}
	return buf, sids, nil
}

func configureRestrictedToken(restricted, base windows.Token, sandboxSID *windows.SID, logonSids []*windows.SID) error {
	mediumSID, err := windows.StringToSid("S-1-16-8192")
	if err != nil {
		return err
	}
	label := windows.Tokenmandatorylabel{
		Label: windows.SIDAndAttributes{Sid: mediumSID, Attributes: windows.SE_GROUP_INTEGRITY},
	}
	if err := windows.SetTokenInformation(restricted, windows.TokenIntegrityLevel, (*byte)(unsafe.Pointer(&label)), label.Size()); err != nil {
		return err
	}
	runtime.KeepAlive(mediumSID)

	user, err := base.GetTokenUser()
	if err != nil {
		return err
	}
	sddl := fmt.Sprintf("D:(A;;GA;;;SY)(A;;GA;;;%s)(A;;GA;;;%s)", user.User.Sid.String(), sandboxSID.String())
	for _, ls := range logonSids {
		sddl += fmt.Sprintf("(A;;GRGX;;;%s)", ls.String())
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err == nil && acl != nil {
		err = windows.SetTokenInformation(restricted, windows.TokenDefaultDacl, (*byte)(unsafe.Pointer(&acl)), uint32(unsafe.Sizeof(acl)))
	}
	runtime.KeepAlive(sd)
	return err
}

// ReinheritHandleDACL restores parent DACL inheritance on a file/directory handle
// moved into the workspace from private staging. It passes an explicitly
// initialized 8-byte empty ACL (never a NULL DACL).
func ReinheritHandleDACL(f *os.File) error {
	if f == nil {
		return fail("invalid_call")
	}
	return reinheritRawHandleDACL(windows.Handle(f.Fd()))
}

func reinheritRawHandleDACL(h windows.Handle) error {
	var emptyACL [8]byte
	ok, _, callErr := procInitializeAcl.Call(uintptr(unsafe.Pointer(&emptyACL[0])), 8, aclRevision)
	if ok == 0 {
		return callErr
	}
	err := windows.SetSecurityInfo(
		h,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil,
		(*windows.ACL)(unsafe.Pointer(&emptyACL[0])),
		nil,
	)
	runtime.KeepAlive(emptyACL)
	return err
}

// EnsureDirGrant idempotently grants (OI)(CI) Modify (without WRITE_DAC/WRITE_OWNER)
// for sandboxSID on managedRoot and repairs protected DACLs on non-reparse,
// single-link existing descendants inside managedRoot.
func EnsureDirGrant(managedRoot string, sandboxSID *windows.SID) error {
	if sandboxSID == nil || managedRoot == "" {
		return fail("invalid_request")
	}
	hRoot, info, err := openOwnedHandle(managedRoot, windows.READ_CONTROL|windows.WRITE_DAC)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(hRoot)
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fail("unsafe_links_detected")
	}

	sd, err := windows.GetSecurityInfo(hRoot, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	oldACL, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if !aclHasGrant(oldACL, sandboxSID, modifyAccessMask) {
		entry := windows.EXPLICIT_ACCESS{
			AccessPermissions: modifyAccessMask,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.CONTAINER_INHERIT_ACE | windows.OBJECT_INHERIT_ACE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(sandboxSID),
			},
		}
		newACL, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, oldACL)
		if err != nil {
			return err
		}
		if err := windows.SetSecurityInfo(hRoot, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, newACL, nil); err != nil {
			return err
		}
		runtime.KeepAlive(newACL)
	}
	runtime.KeepAlive(sd)

	visited := 0
	return walkUnprotectDescendants(managedRoot, sandboxSID, 0, &visited)
}

func walkUnprotectDescendants(dir string, sandboxSID *windows.SID, depth int, visited *int) error {
	if depth > maxGrantDepth || *visited >= maxGrantVisitedEntries {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, item := range entries {
		if *visited >= maxGrantVisitedEntries {
			return nil
		}
		*visited++
		full := filepath.Join(dir, item.Name())
		h, info, err := openOwnedHandle(full, windows.READ_CONTROL|windows.WRITE_DAC)
		if err != nil {
			continue
		}
		isReparse := info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
		isDir := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
		multiLink := !isDir && info.NumberOfLinks > 1
		if isReparse || multiLink {
			_ = windows.CloseHandle(h)
			continue
		}
		sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err == nil {
			ctrl, _, _ := sd.Control()
			dacl, _, daclErr := sd.DACL()
			if ctrl&windows.SE_DACL_PROTECTED != 0 && (daclErr != nil || !aclHasGrant(dacl, sandboxSID, modifyAccessMask)) {
				_ = reinheritRawHandleDACL(h)
			}
			runtime.KeepAlive(sd)
		}
		_ = windows.CloseHandle(h)
		if isDir {
			if err := walkUnprotectDescendants(full, sandboxSID, depth+1, visited); err != nil {
				return err
			}
		}
	}
	return nil
}

func openOwnedHandle(path string, access uint32) (windows.Handle, windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, info, err
	}
	h, err := windows.CreateFile(
		ptr,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return 0, info, err
	}
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		_ = windows.CloseHandle(h)
		return 0, info, err
	}
	return h, info, nil
}

func aclHasGrant(acl *windows.ACL, targetSID *windows.SID, wantMask uint32) bool {
	if acl == nil || targetSID == nil {
		return false
	}
	for i := uint16(0); i < acl.AceCount; i++ {
		var raw *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, uint32(i), &raw); err != nil || raw == nil {
			continue
		}
		if raw.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || raw.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&raw.SidStart))
		if windows.EqualSid(aceSID, targetSID) && uint32(raw.Mask)&wantMask == wantMask {
			return true
		}
	}
	return false
}
