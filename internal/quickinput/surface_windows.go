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

	"github.com/jackgan90/Asterhand/internal/model"
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
type nativeRect struct{ Left, Top, Right, Bottom int32 }
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
	hwnd                          atomic.Uintptr
	ready                         chan error
	done                          chan struct{}
	commands                      chan func()
	initialized                   bool
	closing                       bool
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
	theme                         string
	epoch                         string
	documentReady                 bool
	creationPending               bool
	powerGeneration               uint64
	wantsSuspended                bool
	waiters                       map[string]chan struct{}
	previous                      uintptr
	height                        int
	onMessage                     func(string)
	onFailure                     func(error)
}

func startNativeSurface(home string, onMessage func(string), onFailure func(error), source ...*Draft) (*nativeSurface, error) {
	s := &nativeSurface{home: home, ready: make(chan error, 1), done: make(chan struct{}), commands: make(chan func(), 32), onMessage: onMessage, onFailure: onFailure}
	s.draft, s.theme, s.epoch, s.waiters = &Draft{}, "star", model.ID(), map[string]chan struct{}{}
	if len(source) != 0 {
		s.draft = source[0]
	}
	go s.run()
	select {
	case err := <-s.ready:
		if err != nil {
			if closeErr := s.Close(); closeErr != nil {
				return s, fmt.Errorf("%v；收尾未确认: %w", err, closeErr)
			}
			return nil, err
		}
		return s, err
	case <-time.After(15 * time.Second):
		if err := s.Close(); err != nil {
			return s, fmt.Errorf("WebView2 初始化超时，收尾未确认: %w", err)
		}
		return nil, fmt.Errorf("WebView2 初始化超时")
	}
}

func (s *nativeSurface) run() {
	runtime.LockOSThread()
	// This goroutine owns a dedicated native UI apartment. Returning while
	// locked makes Go terminate the OS thread, releasing WebView2's native TLS
	// caches instead of returning a used COM/UI thread to the scheduler pool.
	defer close(s.done)
	hr, _, _ := oleDLL.NewProc("CoInitializeEx").Call(0, 2)
	if int32(hr) < 0 {
		s.ready <- fmt.Errorf("初始化 STA 失败")
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
		s.ready <- &RegistrationError{Code: "runtime_unavailable", Detail: fmt.Sprintf("需要 WebView2 Evergreen Runtime %s 或更新版本，当前探测版本 %q；请运行安装器补齐", minimum, available)}
		return
	}
	profile := filepath.Join(s.home, "quick-input-profile")
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
	title, _ := windows.UTF16PtrFromString("Asterhand · 快捷输入")
	hwnd, _, err := uiDLL.NewProc("CreateWindowExW").Call(0x80, uintptr(unsafe.Pointer(s.class)), uintptr(unsafe.Pointer(title)), 0x80000000, 0, 0, 680, 240, 0, 0, instance, 0)
	if hwnd == 0 {
		return err
	}
	s.hwnd.Store(hwnd)
	uiHosts.Store(hwnd, s)
	windows.NewLazySystemDLL("wtsapi32.dll").NewProc("WTSRegisterSessionNotification").Call(hwnd, 0)
	return nil
}

func (s *nativeSurface) EnvironmentCompleted(code webviewloader.HRESULT, environment *webviewloader.ICoreWebView2Environment) webviewloader.HRESULT {
	s.creationPending = false
	if s.closing {
		return 0
	}
	if code < 0 || environment == nil {
		s.ready <- fmt.Errorf("WebView2 环境创建失败 0x%x", uint32(code))
		return 0
	}
	s.environment = (*comRef)(unsafe.Pointer(environment)).retain()
	s.version, _ = s.environment.text(5)
	minimum := packaging.Dependencies().WebView2.MinimumRuntimeVersion
	if !runtimeVersionAtLeast(s.version, minimum) {
		s.ready <- fmt.Errorf("WebView2 Runtime %s 过旧，需要 %s 或更新版本", s.version, minimum)
		return 0
	}
	extension, err := s.environment.query("{ee0eb9df-6f12-46ce-b53f-3f47b9c928e0}")
	if err != nil {
		s.ready <- fmt.Errorf("WebView2 不支持独立隐私 profile: %w", err)
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
		s.ready <- fmt.Errorf("WebView2 控制器创建失败 0x%x", uint32(code))
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
		return fmt.Errorf("输入窗口已关闭")
	case <-ctx.Done():
		return ctx.Err()
	default:
		return fmt.Errorf("输入界面正在处理其他操作，请稍后重试")
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
		s.closeError = fmt.Errorf("WebView2 创建仍未完成，请重新启动本节点")
	}
	if s.exitSubscription != nil && !s.browserExited && time.Since(s.closeStarted) < 10*time.Second {
		return
	}
	if s.exitSubscription != nil && !s.browserExited {
		s.closeError = fmt.Errorf("WebView2 浏览器仍在清理，保留 profile")
	}
	uiDLL.NewProc("KillTimer").Call(s.hwnd.Load(), 70)
	uiDLL.NewProc("DestroyWindow").Call(s.hwnd.Load())
}

func (s *nativeSurface) dispose() {
	s.closing = true
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
		s.hide(true)
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
