//go:build windows

package quickinput

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
	"github.com/jackgan90/Asterhand/packaging"
	"github.com/wailsapp/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

var uiDLL = windows.NewLazySystemDLL("user32.dll")
var oleDLL = windows.NewLazySystemDLL("ole32.dll")
var uiHosts sync.Map
var uiSequence atomic.Uint32
var uiWindowProc = windows.NewCallback(surfaceWindowProc)

type nativePoint struct{ X, Y int32 }
type nativeRect = windowBounds
type nativeMessage struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Point          nativePoint
	Private        uint32
}
type nativeWindowClass struct {
	Size, Style                        uint32
	Procedure                          uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	SmallIcon                          uintptr
}

type nativeSurface struct {
	log *logSurface

	hwnd                          atomic.Uintptr
	ready                         chan error
	done                          chan struct{}
	commands                      chan func()
	initialized                   bool
	closing                       bool
	attention                     bool
	environment, controller, core *comRef
	subscriptions                 []eventSubscription
	class                         *uint16
	instance                      uintptr
	home                          string
	version                       string
	browserExited                 bool
	exitSubscription              *eventSubscription
	closeStarted                  time.Time
	closeError                    error
	draft                         *Draft
	remoteDraft                   *editorRelay
	theme                         string
	locale                        string
	epoch                         string
	documentReady                 bool
	pageInitialized               bool
	creationPending               bool
	powerGeneration               uint64
	wantsSuspended                bool
	waiters                       map[string]chan struct{}
	previous                      uintptr
	height                        int
	layoutID                      string
	placements                    map[string]windowPlacement
	placing                       bool
	contentVisible                bool
	icons                         [2]uintptr
	chatSend                      func(hostipc.Message) error
	commandSend                   func(hostipc.Message) error
	onMessage                     func(string)
	onFailure                     func(error)
}

func startNativeSurface(home string, onMessage func(string), onFailure func(error), source ...*Draft) (*nativeSurface, error) {
	return startConfiguredSurface(home, onMessage, onFailure, nil, source...)
}

func startConfiguredSurface(home string, onMessage func(string), onFailure func(error), relay *editorRelay, source ...*Draft) (*nativeSurface, error) {
	return startWindowSurface(home, onMessage, onFailure, relay, nil, source...)
}

func startWindowSurface(home string, onMessage func(string), onFailure func(error), relay *editorRelay, log *logSurface, source ...*Draft) (*nativeSurface, error) {
	s := &nativeSurface{log: log, home: home, ready: make(chan error, 1), done: make(chan struct{}), commands: make(chan func(), 32), onMessage: onMessage, onFailure: onFailure}
	s.remoteDraft = relay
	s.height = initialSurfaceHeight
	s.draft, s.theme, s.locale, s.epoch, s.waiters = &Draft{}, "star", i18n.Effective(), model.ID(), map[string]chan struct{}{}
	if len(source) != 0 {
		s.draft = source[0]
	}
	go s.run()
	select {
	case err := <-s.ready:
		if err != nil {
			if closeErr := s.Close(); closeErr != nil {
				return s, i18n.Wrap("err.quickinput.init_close_unconfirmed", map[string]string{"err": err.Error(), "close_err": closeErr.Error()}, closeErr)
			}
			return nil, err
		}
		return s, err
	case <-time.After(15 * time.Second):
		if err := s.Close(); err != nil {
			return s, i18n.Wrap("err.quickinput.init_timeout_close", nil, err)
		}
		return nil, i18n.Err("err.quickinput.init_timeout")
	}
}

func (s *nativeSurface) run() {
	runtime.LockOSThread()
	// This goroutine owns a dedicated native UI apartment. Returning while
	// locked makes Go terminate the OS thread, releasing WebView2's native TLS
	// caches instead of returning a used COM/UI thread to the scheduler pool.
	defer close(s.done)
	// Production also sets PMv2 at process startup; set the dedicated UI thread
	// explicitly so embedded/test hosts use the same device-coordinate contract.
	uiDLL.NewProc("SetThreadDpiAwarenessContext").Call(^uintptr(3))
	hr, _, _ := oleDLL.NewProc("CoInitializeEx").Call(0, 2)
	if int32(hr) < 0 {
		s.ready <- i18n.Err("err.quickinput.sta_failed")
		return
	}
	defer oleDLL.NewProc("CoUninitialize").Call()
	if err := s.createWindow(); err != nil {
		s.ready <- err
		return
	}
	defer s.dispose()
	available, err := webviewloader.GetAvailableCoreWebView2BrowserVersionString("")
	minimum := packaging.Dependencies().WebView2.MinimumRuntimeVersion
	if err != nil || !runtimeVersionAtLeast(available, minimum) {
		args := map[string]string{"minimum": minimum, "available": strconv.Quote(available)}
		s.ready <- &RegistrationError{
			Code:   "runtime_unavailable",
			Key:    "err.quickinput.webview2_version_need",
			Args:   args,
			Detail: i18n.T(i18n.Effective(), "err.quickinput.webview2_version_need", args),
		}
		return
	}
	profile := filepath.Join(s.home, "quick-input-profile")
	if s.log != nil {
		profile = filepath.Join(s.home, "command-log-profiles", s.log.id)
	}
	if err := os.MkdirAll(profile, 0700); err != nil {
		s.ready <- err
		return
	}
	s.creationPending = true
	if err := webviewloader.CreateCoreWebView2EnvironmentWithOptions(s, webviewloader.WithUserDataFolder(profile)); err != nil {
		s.creationPending = false
		s.ready <- err
		return
	}
	var msg nativeMessage
	for {
		result, _, _ := uiDLL.NewProc("GetMessageW").Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(result) <= 0 {
			return
		}
		uiDLL.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&msg)))
		uiDLL.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func (s *nativeSurface) createWindow() error {
	instance, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
	s.instance = instance
	s.class, _ = windows.UTF16PtrFromString(fmt.Sprintf("Asterhand.QuickInput.%d.%d", os.Getpid(), uiSequence.Add(1)))
	cls := nativeWindowClass{Procedure: uiWindowProc, Instance: instance, ClassName: s.class}
	cls.Size = uint32(unsafe.Sizeof(cls))
	if ok, _, err := uiDLL.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&cls))); ok == 0 {
		return err
	}
	title, _ := windows.UTF16PtrFromString(i18n.T(s.locale, s.titleKey()))
	const appWindow = 0x00040000                                 // WS_EX_APPWINDOW; no owner or TOOLWINDOW suppression.
	windowStyle := uintptr(0x80000000 | 0x00080000 | 0x00020000) // POPUP | SYSMENU | MINIMIZEBOX
	if s.log != nil {
		windowStyle = 0x00cf0000
	}
	hwnd, _, err := uiDLL.NewProc("CreateWindowExW").Call(appWindow, uintptr(unsafe.Pointer(s.class)), uintptr(unsafe.Pointer(title)), windowStyle, 0, 0, 680, initialSurfaceHeight, 0, 0, instance, 0)
	if hwnd == 0 {
		return err
	}
	s.hwnd.Store(hwnd)
	if s.log == nil {
		s.positionForShow()
	} else {
		uiDLL.NewProc("SetWindowPos").Call(hwnd, 0, 160, 120, 920, 640, 0)
	}
	uiHosts.Store(hwnd, s)
	s.setWindowIcons()
	windows.NewLazySystemDLL("wtsapi32.dll").NewProc("WTSRegisterSessionNotification").Call(hwnd, 0)
	return nil
}

func (s *nativeSurface) EnvironmentCompleted(code webviewloader.HRESULT, environment *webviewloader.ICoreWebView2Environment) webviewloader.HRESULT {
	s.creationPending = false
	if s.closing {
		return 0
	}
	if code < 0 || environment == nil {
		s.ready <- i18n.ErrArgs("err.quickinput.env_create_failed", map[string]string{"code": fmt.Sprintf("%x", uint32(code))})
		return 0
	}
	s.environment = (*comRef)(unsafe.Pointer(environment)).retain()
	s.version, _ = s.environment.text(5)
	minimum := packaging.Dependencies().WebView2.MinimumRuntimeVersion
	if !runtimeVersionAtLeast(s.version, minimum) {
		s.ready <- i18n.ErrArgs("err.quickinput.webview2_too_old", map[string]string{"version": s.version, "minimum": minimum})
		return 0
	}
	extension, err := s.environment.query("{ee0eb9df-6f12-46ce-b53f-3f47b9c928e0}")
	if err != nil {
		s.ready <- i18n.Wrap("err.quickinput.profile_unsupported", nil, err)
		return 0
	}
	defer extension.release()
	exitEvent, err := subscribe[browserExitCallback](extension, 12, func(_ uintptr, _ *comRef) uintptr { s.browserExited = true; return 0 })
	if err != nil {
		s.ready <- err
		return 0
	}
	extension.retain()
	closeExitHandler := exitEvent.close
	exitEvent.close = func() error { defer extension.release(); return closeExitHandler() }
	s.exitSubscription = &exitEvent
	options, err := extension.object(20)
	if err != nil {
		s.ready <- err
		return 0
	}
	defer options.release()
	if err = options.call(6, 1); err != nil {
		s.ready <- err
		return 0
	}
	if err = options.putText(4, "AsterhandQuickInput"); err != nil {
		s.ready <- err
		return 0
	}
	handler := newCallback[controllerCallback](s.controllerCompleted)
	defer handler.Close()
	s.creationPending = true
	if err = extension.call(21, s.hwnd.Load(), uintptr(unsafe.Pointer(options)), handler.Ref()); err != nil {
		s.creationPending = false
		s.ready <- err
	}
	return 0
}

func runtimeVersionAtLeast(actual, minimum string) bool {
	a, b := strings.Split(actual, "."), strings.Split(minimum, ".")
	if len(a) != 4 || len(b) != 4 {
		return false
	}
	for i := range a {
		av, ae := strconv.Atoi(a[i])
		bv, be := strconv.Atoi(b[i])
		if ae != nil || be != nil || av < 0 || bv < 0 {
			return false
		}
		if av != bv {
			return av > bv
		}
	}
	return true
}

func (s *nativeSurface) controllerCompleted(code uintptr, value *comRef) uintptr {
	s.creationPending = false
	if s.closing {
		if value != nil {
			_ = value.call(24)
		}
		return 0
	}
	if int32(code) < 0 || value == nil {
		s.ready <- i18n.ErrArgs("err.quickinput.controller_create_failed", map[string]string{"code": fmt.Sprintf("%x", uint32(code))})
		return 0
	}
	s.controller = value.retain()
	core, err := s.controller.object(25)
	if err == nil {
		s.core = core
		err = s.configureContent()
	}
	if err != nil {
		s.ready <- err
		return 0
	}
	_ = s.controller.call(4, 0)
	var rect nativeRect
	uiDLL.NewProc("GetClientRect").Call(s.hwnd.Load(), uintptr(unsafe.Pointer(&rect)))
	_ = s.controller.call(6, uintptr(unsafe.Pointer(&rect)))
	s.initialized = true
	return 0
}

func (s *nativeSurface) dispatch(ctx context.Context, fn func()) error {
	select {
	case s.commands <- fn:
		uiDLL.NewProc("PostMessageW").Call(s.hwnd.Load(), 0x8021, 0, 0)
		return nil
	case <-s.done:
		return i18n.Err("err.quickinput.window_closed")
	case <-ctx.Done():
		return ctx.Err()
	default:
		return i18n.Err("err.quickinput.surface_busy_retry")
	}
}

func (s *nativeSurface) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	select {
	case <-s.done:
		return s.closeError
	default:
	}
	_ = s.dispatch(ctx, s.beginClose)
	select {
	case <-s.done:
		return s.closeError
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *nativeSurface) beginClose() {
	if s.closing {
		return
	}
	s.closing = true
	s.hide(true)
	s.closeStarted = time.Now()
	for i := len(s.subscriptions) - 1; i >= 0; i-- {
		s.subscriptions[i].unsubscribe()
	}
	s.subscriptions = nil
	if s.controller != nil {
		s.closeError = s.controller.call(24)
	}
	s.core.release()
	s.controller.release()
	s.core, s.controller = nil, nil
	// Keep pumping STA messages until WebView2 reports that its browser released UDF.
	uiDLL.NewProc("SetTimer").Call(s.hwnd.Load(), 70, 25, 0)
}

func (s *nativeSurface) finishClose() {
	if s.creationPending && time.Since(s.closeStarted) < 10*time.Second {
		return
	}
	if s.creationPending {
		s.closeError = i18n.Err("err.quickinput.create_unfinished")
	}
	if s.exitSubscription != nil && !s.browserExited && time.Since(s.closeStarted) < 10*time.Second {
		return
	}
	if s.exitSubscription != nil && !s.browserExited {
		s.closeError = i18n.Err("err.quickinput.browser_cleaning")
	}
	uiDLL.NewProc("KillTimer").Call(s.hwnd.Load(), 70)
	uiDLL.NewProc("DestroyWindow").Call(s.hwnd.Load())
}

func (s *nativeSurface) dispose() {
	s.closing = true
	s.stopAttention()
	for i := len(s.subscriptions) - 1; i >= 0; i-- {
		s.subscriptions[i].unsubscribe()
	}
	if s.controller != nil {
		_ = s.controller.call(24)
	}
	s.core.release()
	s.controller.release()
	if s.exitSubscription != nil {
		s.exitSubscription.unsubscribe()
	}
	s.environment.release()
	windows.NewLazySystemDLL("wtsapi32.dll").NewProc("WTSUnRegisterSessionNotification").Call(s.hwnd.Load())
	if hwnd := s.hwnd.Swap(0); hwnd != 0 {
		uiHosts.Delete(hwnd)
		uiDLL.NewProc("DestroyWindow").Call(hwnd)
	}
	uiDLL.NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(s.class)), s.instance)
	s.releaseWindowIcons()
}

func surfaceWindowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	entry, ok := uiHosts.Load(hwnd)
	if !ok {
		r, _, _ := uiDLL.NewProc("DefWindowProcW").Call(hwnd, uintptr(message), wParam, lParam)
		return r
	}
	s := entry.(*nativeSurface)
	switch message {
	case 0x8021:
		select {
		case fn := <-s.commands:
			fn()
		default:
		}
	case 0x0010:
		if s.log != nil {
			s.beginClose()
		} else {
			s.hide(true)
		}
	case 0x0002:
		uiDLL.NewProc("PostQuitMessage").Call(0)
	case 0x0113:
		if wParam == 70 {
			s.finishClose()
		}
	default:
		return s.windowEvent(hwnd, message, wParam, lParam)
	}
	return 0
}
