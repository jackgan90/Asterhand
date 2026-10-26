//go:build windows

package quickinput

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sync/atomic"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
	"golang.org/x/sys/windows"
)

// RunInputHost is the dedicated GUI entry point. It never opens node settings,
// credentials, a service listener, a database, or an executor.
func RunInputHost(args []string) error {
	flags := flag.NewFlagSet("input-host", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	inputHandle := flags.String("command-handle", "", "inherited command pipe")
	outputHandle := flags.String("status-handle", "", "inherited status pipe")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *inputHandle == *outputHandle {
		return hostipc.ErrProtocol
	}
	input, err := inheritedPipe(*inputHandle, "input-host-commands")
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := inheritedPipe(*outputHandle, "input-host-status")
	if err != nil {
		return err
	}
	defer output.Close()
	if err = verifyHostToken(windows.GetCurrentProcessToken()); err != nil {
		return err
	}
	if err = checkInteractiveDesktop(); err != nil {
		return err
	}
	uiDLL.NewProc("SetProcessDpiAwarenessContext").Call(^uintptr(3))
	initial, err := hostipc.Read(input, false)
	if err != nil || initial.Kind != hostipc.Initialize || initial.AppVersion != config.Version || initial.Source != config.SourceDigest || !filepath.IsAbs(initial.Home) {
		return hostipc.ErrProtocol
	}
	return serveInputHost(input, output, initial)
}

func serveInputHost(input io.Reader, output io.Writer, initial hostipc.Message) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer := newHostPeer(ctx, input, output, initial.Session)
	send, fail := peer.send, peer.fail
	if err := checkHostRuntimePolicy(); err != nil {
		peer.terminalFailure("runtime_override")
		return err
	}
	relay := newEditorRelay(initial.Epoch, initial.Revision, send, fail)
	draft := &Draft{text: initial.Text, revision: initial.Revision}
	failed := func(err error) {
		var native *RegistrationError
		if errors.As(err, &native) && native.Code == "focus_denied" {
			_ = send(hostipc.Message{Kind: hostipc.Failure, Code: native.Code})
			return
		}
		fail(err)
	}
	surface, err := startConfiguredSurface(initial.Home, nil, failed, relay, draft)
	if err != nil {
		peer.terminalFailure(runtimeFailureCode(err))
		return err
	}
	defer surface.Close()
	if err = surface.Update(initial.Theme); err != nil {
		return err
	}
	if initial.Recovered {
		_ = surface.dispatch(ctx, func() {
			_ = surface.postJSON(map[string]string{"kind": "error", "message": "输入界面曾中断，已恢复主进程最后接收的草稿；请检查未完成的输入。"})
		})
	}
	if err = send(hostipc.Message{Kind: hostipc.Ready, AppVersion: config.Version, Source: config.SourceDigest, Runtime: surface.version}); err != nil {
		return err
	}
	var snapshotBusy atomic.Bool
	for {
		select {
		case err := <-peer.failures:
			peer.terminalFailure("renderer_failed")
			return err
		case <-surface.done:
			return fmt.Errorf("input window closed")
		case m := <-peer.in:
			if m.Kind == hostipc.Quit {
				return surface.Close()
			}
			if err := serveHostCommand(ctx, surface, relay, &snapshotBusy, m, send, fail); err != nil {
				return err
			}
		}
	}
}

func serveHostCommand(ctx context.Context, surface *nativeSurface, relay *editorRelay, busy *atomic.Bool, m hostipc.Message, send func(hostipc.Message) error, fail func(error)) error {
	switch m.Kind {
	case hostipc.Accepted:
		return relay.accept(m)
	case hostipc.Toggle:
		surface.Toggle()
		return surface.dispatch(ctx, func() { _ = send(hostipc.Message{Kind: hostipc.State, Request: m.Request}) })
	case hostipc.Theme:
		if err := surface.Update(m.Theme); err != nil {
			return err
		}
		return surface.dispatch(ctx, func() { _ = send(hostipc.Message{Kind: hostipc.State, Request: m.Request}) })
	case hostipc.Snapshot:
		if !busy.CompareAndSwap(false, true) {
			return hostipc.ErrProtocol
		}
		go func() {
			defer busy.Store(false)
			result := hostipc.Message{Kind: hostipc.SnapshotDone, Request: m.Request}
			if err := surface.Snapshot(ctx); err != nil {
				result.Code = "snapshot_failed"
			}
			result.Epoch, result.Sequence, result.Revision = relay.state()
			if err := send(result); err != nil {
				fail(err)
			}
		}()
		return nil
	default:
		return hostipc.ErrProtocol
	}
}

func runtimeFailureCode(err error) string {
	var native *RegistrationError
	if errors.As(err, &native) && native.Code == "runtime_unavailable" {
		return native.Code
	}
	return "runtime_initialization_failed"
}
