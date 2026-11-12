//go:build windows

package userprocess

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func JobProcessCount(job windows.Handle) (uint32, error) {
	var info struct {
		User, Kernel, PeriodUser, PeriodKernel int64
		Faults, Total, Active, Terminated      uint32
	}
	err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
	return info.Active, err
}

// JobStopped checks a durable, unpredictable job-object identity. It never
// infers process identity from a recycled PID and never terminates a process.
func JobStopped(name string) (bool, error) {
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false, err
	}
	raw, _, callErr := windows.NewLazySystemDLL("kernel32.dll").NewProc("OpenJobObjectW").Call(4, 0, uintptr(unsafe.Pointer(ptr)))
	job := windows.Handle(raw)
	if job == 0 {
		if callErr == windows.ERROR_FILE_NOT_FOUND {
			return true, nil
		}
		return false, callErr
	}
	defer windows.CloseHandle(job)
	count, err := JobProcessCount(job)
	return count == 0, err
}
