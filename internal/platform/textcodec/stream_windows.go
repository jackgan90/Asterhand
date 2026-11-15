//go:build windows

package textcodec

import "golang.org/x/sys/windows"

var (
	procGetOEMCP = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetOEMCP")
	procGetACP   = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetACP")
)

// DefaultLegacyEncoding inspects Windows console OEM/ANSI codepages and returns
// the matching WHATWG legacy encoding fallback when child output is not UTF-8.
func DefaultLegacyEncoding() string {
	for _, proc := range []*windows.LazyProc{procGetOEMCP, procGetACP} {
		if err := proc.Find(); err == nil {
			cp, _, _ := proc.Call()
			switch cp {
			case 950:
				return "big5"
			case 936, 54936:
				return "gbk"
			}
		}
	}
	return "gbk"
}
