//go:build windows

package quickinput

import (
	"fmt"
	"runtime"
	"strings"
	"unicode/utf16"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/conversation/wire"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
	"golang.org/x/sys/windows"
)

func (s *nativeSurface) chatRequest(message bridgeMessage) {
	if message.Chat == nil || !wire.ValidID(message.ID) || s.chatSend == nil || s.remoteDraft == nil {
		return
	}
	if err := message.Chat.Validate(); err != nil {
		_ = s.postJSON(map[string]any{"kind": "chat_result", "id": message.ID,
			"reply": hostipc.ChatReply{Response: wire.Response{Error: wire.PublicError(err)}}})
		return
	}
	request := *message.Chat
	send := s.chatSend
	relay := s.remoteDraft
	deliver := func() {
		epoch, sequence, revision := relay.state()
		if request.Action == "submit" && sequence != request.DraftRevision {
			// A newer coalesced edit won. The original snapshot may still be sent,
			// but it is no longer eligible to clear a restored parent draft.
			revision = 0
		}
		if request.Action == "submit" {
			sequence = request.DraftRevision
		}
		_ = send(hostipc.Message{Kind: hostipc.ChatCall, ChatID: message.ID, Chat: &request,
			Epoch: epoch, Sequence: sequence, Revision: revision})
	}
	if request.Action == "submit" {
		if request.DraftRevision != message.Revision {
			return
		}
		relay.submit(request.Text, request.DraftRevision, "chat-"+message.ID, deliver)
	} else {
		deliver()
	}
}

func writeChatClipboard(window uintptr, text string) error {
	if strings.ContainsRune(text, 0) {
		return fmt.Errorf("clipboard text contains NUL")
	}
	opened, _, _ := uiDLL.NewProc("OpenClipboard").Call(window)
	if opened == 0 {
		return fmt.Errorf("clipboard busy")
	}
	defer uiDLL.NewProc("CloseClipboard").Call()
	units := append(utf16.Encode([]rune(text)), 0)
	kernel := windows.NewLazySystemDLL("kernel32.dll")
	memory, _, _ := kernel.NewProc("GlobalAlloc").Call(0x0002, uintptr(len(units)*2))
	if memory == 0 {
		return fmt.Errorf("clipboard allocation failed")
	}
	owned := true
	defer func() {
		if owned {
			kernel.NewProc("GlobalFree").Call(memory)
		}
	}()
	pointer, _, _ := kernel.NewProc("GlobalLock").Call(memory)
	if pointer == 0 {
		return fmt.Errorf("clipboard allocation lock failed")
	}
	kernel.NewProc("RtlMoveMemory").Call(pointer, uintptr(unsafe.Pointer(&units[0])), uintptr(len(units)*2))
	runtime.KeepAlive(units)
	kernel.NewProc("GlobalUnlock").Call(memory)
	if ok, _, _ := uiDLL.NewProc("EmptyClipboard").Call(); ok == 0 {
		return fmt.Errorf("clipboard clear failed")
	}
	if handle, _, _ := uiDLL.NewProc("SetClipboardData").Call(13, memory); handle == 0 {
		return fmt.Errorf("clipboard set failed")
	}
	owned = false // Windows now owns the movable allocation.
	return nil
}
