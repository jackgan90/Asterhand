//go:build windows

package workspace

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Every ancestor is pinned without FILE_SHARE_DELETE. Relative handle lookups
// reject a directory turned into a reparse point even while it remains pinned.
// Existing content is held
// without write/delete sharing and is never modified in place: Windows allows
// new hard links even under an exclusive handle. Replacement content is prepared
// privately, then published with a non-overwriting handle rename.
type heldFile struct {
	file    *os.File
	parents []*os.File
	name    string
	removed bool
	staging string
	retired []stagedFile
	missing []string
}

type stagedFile struct {
	file *os.File
	name string
}

func (h *heldFile) close() {
	if h.file != nil {
		h.file.Close()
		h.file = nil
	}
	for _, staged := range h.retired {
		staged.file.Close()
		_ = os.Remove(staged.name)
	}
	h.retired = nil
	for i := len(h.parents) - 1; i >= 0; i-- {
		h.parents[i].Close()
	}
	h.parents = nil
}

func openHandle(name string, access, share, disposition uint32) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(extendedPath(name))
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, access, share, nil, disposition, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, err
	}
	return checkedHandle(h, name)
}

// Each child is opened relative to its pinned parent. Reparse-point changes
// after validation must never redirect a later absolute-path lookup.
func openChild(parent *os.File, name string, access, share uint32, createDirectory bool) (*os.File, error) {
	un, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	oa := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.Fd()), ObjectName: un, Attributes: windows.OBJ_CASE_INSENSITIVE}
	oa.Length = uint32(unsafe.Sizeof(oa))
	options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT | windows.FILE_OPEN_FOR_BACKUP_INTENT)
	disposition := uint32(windows.FILE_OPEN)
	if createDirectory {
		options |= windows.FILE_DIRECTORY_FILE
		disposition = windows.FILE_OPEN_IF
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	if err = windows.NtCreateFile(&handle, access|windows.SYNCHRONIZE, &oa, &status, nil, windows.FILE_ATTRIBUTE_NORMAL, share, disposition, options, 0, 0); err != nil {
		if status, ok := err.(windows.NTStatus); ok {
			return nil, status.Errno()
		}
		return nil, err
	}
	f, err := checkedHandle(handle, filepath.Join(parent.Name(), name))
	if err != nil {
		return nil, err
	}
	if actual, nameErr := canonicalHandleBase(windows.Handle(f.Fd())); nameErr != nil || !strings.EqualFold(actual, name) {
		f.Close()
		if nameErr != nil {
			return nil, nameErr
		}
		return nil, fail("invalid_path")
	}
	return f, nil
}

func canonicalHandleBase(h windows.Handle) (string, error) {
	const fileNameNormalized = uint32(0)
	var stack [windows.MAX_PATH]uint16
	n, err := windows.GetFinalPathNameByHandle(h, &stack[0], uint32(len(stack)), fileNameNormalized)
	if err != nil || n == 0 {
		if err != nil {
			return "", err
		}
		return "", fail("invalid_path")
	}
	if n < uint32(len(stack)) {
		return filepath.Base(windows.UTF16ToString(stack[:n])), nil
	}
	buf := make([]uint16, n+1)
	written, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), fileNameNormalized)
	if err != nil || written == 0 || written >= uint32(len(buf)) {
		if err != nil {
			return "", err
		}
		return "", fail("invalid_path")
	}
	return filepath.Base(windows.UTF16ToString(buf[:written])), nil
}

func checkedHandle(h windows.Handle, name string) (*os.File, error) {
	f := os.NewFile(uintptr(h), name)
	var info windows.ByHandleFileInformation
	err := windows.GetFileInformationByHandle(h, &info)
	if err == nil {
		if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.NumberOfLinks > 1 {
			err = fail("unsafe_link")
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func pinParents(root, name string, create bool) (*heldFile, error) {
	full := filepath.Join(root, filepath.FromSlash(name))
	volume := filepath.VolumeName(full)
	if len(volume) != 2 || volume[1] != ':' {
		return nil, fail("unsupported_path")
	}
	h := &heldFile{name: full}
	parent := filepath.Dir(full)
	current := volume + `\`
	parts := strings.Split(strings.TrimPrefix(parent, current), string(filepath.Separator))
	for i := -1; i < len(parts); i++ {
		if i >= 0 && parts[i] != "" {
			current = filepath.Join(current, parts[i])
		}
		access := uint32(windows.FILE_READ_ATTRIBUTES)
		if strings.EqualFold(current, parent) {
			access |= windows.GENERIC_READ
		}
		var f *os.File
		var err error
		if len(h.parents) == 0 {
			f, err = openHandle(current, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, windows.OPEN_EXISTING)
		} else {
			f, err = openChild(h.parents[len(h.parents)-1], filepath.Base(current), access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, false)
		}
		if os.IsNotExist(err) && create && len(current) > len(root) {
			h.missing = append(h.missing, current)
			for _, part := range parts[i+1:] {
				current = filepath.Join(current, part)
				h.missing = append(h.missing, current)
			}
			return h, nil
		}
		if err != nil {
			h.close()
			return nil, err
		}
		st, err := f.Stat()
		if err != nil || !st.IsDir() {
			f.Close()
			h.close()
			return nil, fail("invalid_directory")
		}
		h.parents = append(h.parents, f)
	}
	return h, nil
}

// Admission only records missing parents. Creation starts after the durable
// operation exists and the service has reserved their capacity.
func (h *heldFile) pinAvailableParents() error {
	for len(h.missing) > 0 {
		name := h.missing[0]
		f, err := openChild(h.parents[len(h.parents)-1], filepath.Base(name), windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, false)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil || !info.IsDir() {
			f.Close()
			return fail("invalid_directory")
		}
		h.parents = append(h.parents, f)
		h.missing = h.missing[1:]
	}
	return nil
}

func (h *heldFile) createParents() error {
	for len(h.missing) > 0 {
		name := h.missing[0]
		f, err := openChild(h.parents[len(h.parents)-1], filepath.Base(name), windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, true)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil || !info.IsDir() {
			f.Close()
			return fail("invalid_directory")
		}
		h.parents = append(h.parents, f)
		h.missing = h.missing[1:]
	}
	// On partial failure the held parents are still released by closeCall.
	return nil
}

func hold(root, name string, writing, createParents bool) (*heldFile, error) {
	h, err := pinParents(root, name, createParents)
	if err != nil {
		return nil, err
	}
	access := uint32(windows.GENERIC_READ)
	share := uint32(windows.FILE_SHARE_READ)
	if writing {
		access |= windows.GENERIC_WRITE | windows.DELETE
		share = 0
	}
	if len(h.missing) > 0 {
		return h, nil
	}
	h.file, err = openChild(h.parents[len(h.parents)-1], filepath.Base(h.name), access, share, false)
	if os.IsNotExist(err) {
		return h, nil
	}
	if err != nil {
		h.close()
		return nil, err
	}
	st, err := h.file.Stat()
	if err == nil && st.IsDir() {
		err = fail("not_file")
	}
	if err != nil {
		h.close()
		return nil, err
	}
	return h, nil
}

func (h *heldFile) bytes(limit int64) ([]byte, error) {
	if h.file == nil || h.removed {
		return nil, os.ErrNotExist
	}
	st, err := h.file.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > limit {
		return nil, fail("file_too_large")
	}
	data := make([]byte, st.Size())
	_, err = h.file.ReadAt(data, 0)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return data, err
}

func (h *heldFile) observe() (string, bool, error) {
	if h.removed {
		return MissingRevision, false, nil
	}
	if h.file != nil {
		data, err := h.bytes(MaxFileBytes)
		return revision(data), false, err
	}
	// A non-overwriting publication can lose a vacant name to an external
	// creator. Report that actual content as a conflict, never as our artifact.
	if err := h.pinAvailableParents(); err != nil {
		return "unknown", true, err
	}
	if len(h.missing) > 0 {
		return MissingRevision, false, nil
	}
	f, err := openChild(h.parents[len(h.parents)-1], filepath.Base(h.name), windows.GENERIC_READ, 0, false)
	if os.IsNotExist(err) {
		return MissingRevision, false, nil
	}
	if err != nil {
		return "unknown", true, err
	}
	defer f.Close()
	current := heldFile{file: f}
	data, err := current.bytes(MaxFileBytes)
	return revision(data), true, err
}

func (h *heldFile) write(data []byte) error {
	if h.removed {
		return fail("already_removed")
	}
	if h.staging == "" {
		return fail("invalid_call")
	}
	if err := h.createParents(); err != nil {
		return err
	}
	name := filepath.Join(h.staging, rand.Text()+".new")
	f, err := openHandle(name, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE, 0, windows.CREATE_NEW)
	if err != nil {
		return fmt.Errorf("create staged content: %w", err)
	}
	defer func() {
		if f != nil {
			f.Close()
			_ = os.Remove(name)
		}
	}()
	if _, err = f.WriteAt(data, 0); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if h.file != nil {
		oldName := filepath.Join(h.staging, rand.Text()+".old")
		if err = renameHandle(h.file, oldName); err != nil {
			return err
		}
		h.retired = append(h.retired, stagedFile{file: h.file, name: oldName})
		h.file = nil
	}
	// An outside program can win the newly-vacated name. Never overwrite it;
	// the original bytes remain in the durable preimage and private retired file.
	if err = renameHandleIn(f, filepath.Base(h.name), h.parents[len(h.parents)-1]); err != nil {
		if os.IsExist(err) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return fail("revision_conflict")
		}
		return fmt.Errorf("publish staged content: %w", err)
	}
	h.file, f = f, nil
	return nil
}

func renameHandle(file *os.File, destination string) error {
	return renameHandleIn(file, extendedPath(destination), nil)
}

func renameHandleIn(file *os.File, destination string, parent *os.File) error {
	name, err := windows.UTF16FromString(destination)
	if err != nil {
		return err
	}
	name = name[:len(name)-1]
	type renameInfo struct {
		Replace uint32
		Root    windows.Handle
		Length  uint32
		Name    [1]uint16
	}
	offset := unsafe.Offsetof(renameInfo{}.Name)
	// Keep the terminating UTF-16 NUL and the structure's trailing alignment.
	// FileNameLength excludes that NUL, while BufferSize includes it.
	buffer := make([]byte, int(unsafe.Sizeof(renameInfo{}))+len(name)*2)
	info := (*renameInfo)(unsafe.Pointer(&buffer[0]))
	if parent != nil {
		info.Root = windows.Handle(parent.Fd())
	}
	info.Length = uint32(len(name) * 2)
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buffer[offset])), len(name)), name)
	if parent != nil {
		var status windows.IO_STATUS_BLOCK
		if err := windows.NtSetInformationFile(windows.Handle(file.Fd()), &status, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation); err != nil {
			if code, ok := err.(windows.NTStatus); ok {
				return code.Errno()
			}
			return err
		}
		return nil
	}
	return windows.SetFileInformationByHandle(windows.Handle(file.Fd()), windows.FileRenameInfo, &buffer[0], uint32(len(buffer)))
}

// Inputs are absolute drive paths constructed from a validated relative name.
// The prefix only enables long names; callers can never supply a namespace.
func extendedPath(name string) string {
	if strings.HasPrefix(name, `\\?\`) {
		return name
	}
	return `\\?\` + name
}

func (h *heldFile) remove() error {
	if h.file == nil || h.removed {
		return os.ErrNotExist
	}
	flag := byte(1)
	if err := windows.SetFileInformationByHandle(windows.Handle(h.file.Fd()), windows.FileDispositionInfo, &flag, uint32(unsafe.Sizeof(flag))); err != nil {
		return err
	}
	h.removed = true
	return nil
}

func directory(root, name string) (*heldFile, error) {
	// A synthetic child pins the requested directory as an ancestor.
	return pinParents(root, filepath.Join(name, ".asterhand-directory-sentinel"), false)
}

func platformSupported() bool { return true }
