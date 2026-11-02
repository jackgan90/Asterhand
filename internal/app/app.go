package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/executor"
	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/localcommands"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/peer"
	"github.com/jackgan90/Asterhand/internal/quickinput"
	"github.com/jackgan90/Asterhand/internal/store"
	"github.com/jackgan90/Asterhand/internal/tasks"
)

type session struct {
	expires time.Time
	csrf    string
}
type App struct {
	Config          *config.Manager
	Store           *store.Store
	Peers           *peer.Manager
	Engine          *tasks.Engine
	Input           *quickinput.Controller
	Chat            *conversation.Service
	Commands        *localcommands.Registry
	CommandLogs     quickinput.LogWindowHost
	pickScript      func(context.Context) (string, error)
	commandPicking  bool
	desktopRequests chan struct{}
	loginToken      string
	mu              sync.Mutex
	sessions        map[string]session
	probeTokens     map[string]bool
	probeRunning    bool
	probeStatus     string
	probeError      string
	probeErrorKey   string
	probeErrorArgs  map[string]string
	webAddress      string
	peerAddress     string
	wg              sync.WaitGroup
	cancel          context.CancelFunc
	done            <-chan struct{}
	ctx             context.Context
	peerSync        chan struct{}
	refreshMu       sync.Mutex
	refreshCh       chan struct{}
	requests        requestGate
	stopping        bool
	closeOnce       sync.Once
	closeErr        error
}

func New(c *config.Manager, x executor.Executor) (*App, error) {
	s, err := store.Open(filepath.Join(c.Home, "asterhand.db"))
	if err != nil {
		return nil, err
	}
	p, err := peer.Open(c, s)
	if err != nil {
		s.Close()
		return nil, err
	}
	if x == nil {
		if err := executor.ValidateSelection(c.Get()); err != nil {
			s.Close()
			return nil, err
		}
		if err := executor.InvalidateStaleVerification(c); err != nil {
			s.Close()
			return nil, err
		}
		x = &executor.Selected{Config: c}
	}
	a := &App{Config: c, Store: s, Peers: p, sessions: map[string]session{}, probeTokens: map[string]bool{}, probeStatus: "not_run", peerSync: make(chan struct{}, 1), refreshCh: make(chan struct{})}
	a.Engine = tasks.New(s, c, x)
	a.Engine.SubmitRemote = a.submit
	tokenPath := filepath.Join(c.Home, "web-login.token")
	b, err := os.ReadFile(tokenPath)
	if errors.Is(err, os.ErrNotExist) {
		b = []byte(model.ID())
		err = config.AtomicFile(tokenPath, b, 0600)
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	a.loginToken = string(b)
	a.Input = quickinput.New(c.Home)
	a.Commands = localcommands.Open(c.Home, quickinput.StartLocalCommand)
	if err = a.Commands.ConfigureManaged(localcommands.NewRunManager(c.Home, localcommands.NewPlatformBackend(), nil)); err != nil {
		log.Printf("%s", i18n.T(i18n.Effective(), "err.localcmd.managed_unavailable"))
	}
	a.Input.SetCommandHandler(a.commandCall)
	a.CommandLogs = quickinput.NewLogWindowHost(c.Home, a.commandCall)
	preferences := a.Input.Status()
	a.CommandLogs.Update(preferences.Desired.Theme, preferences.EffectiveLocale)
	a.Chat = conversation.New(c.Get().NodeID, s, executor.NewConversationRuntime(c))
	a.Input.SetConversationClient(conversation.Client{Target: a.Chat.Target(), Transport: a.Chat})
	a.Chat.SetCompletionHandler(a.Input.ReplyCompleted)
	a.desktopRequests = make(chan struct{}, 8)
	return a, nil
}
func (a *App) Start(ctx context.Context) (string, error) {
	ctx, a.cancel = context.WithCancel(ctx)
	a.done = ctx.Done()
	a.ctx = ctx
	c := a.Config.Get()
	host, _, err := net.SplitHostPort(c.WebListen)
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", i18n.Err("err.app.web_loopback_only")
	}
	web, err := net.Listen("tcp", c.WebListen)
	if err != nil {
		return "", err
	}
	remote, err := net.Listen("tcp", c.PeerListen)
	if err != nil {
		web.Close()
		return "", err
	}
	a.webAddress = web.Addr().String()
	a.peerAddress = remote.Addr().String()
	a.Engine.Callback = "http://" + a.webAddress + "/internal/tool"
	if err = a.Engine.Recover(); err != nil {
		web.Close()
		remote.Close()
		return "", err
	}
	a.Chat.Start()
	localServer := &http.Server{Handler: a.requests.wrap(a.local), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024}
	peerServer := &http.Server{Handler: a.requests.wrap(a.remote), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024}
	tlsListener := tls.NewListener(remote, &tls.Config{Certificates: []tls.Certificate{a.Peers.TLS}, MinVersion: tls.VersionTLS13})
	for _, run := range []func(){func() { localServer.Serve(web) }, func() { peerServer.Serve(tlsListener) }, func() {
		if err := a.Engine.Run(ctx); err != nil {
			log.Printf("%s", i18n.T(i18n.Effective(), "err.app.persist_failed_log", map[string]string{"err": err.Error()}))
			a.RequestStop(true)
		}
	}, func() { a.outboxLoop(ctx) }, func() { a.peerSyncLoop(ctx) }} {
		a.wg.Add(1)
		go func(fn func()) { defer a.wg.Done(); fn() }(run)
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		<-ctx.Done()
		a.RequestStop(true)
		localServer.Close()
		peerServer.Close()
	}()
	return fmt.Sprintf("http://%s/#login=%s", a.webAddress, a.loginToken), nil
}
func (a *App) Close() error {
	a.closeOnce.Do(func() {
		a.RequestStop(true)
		a.requests.wg.Wait()
		a.wg.Wait()
		chatErr := a.Chat.Close()
		commandCtx, commandCancel := context.WithTimeout(context.Background(), 15*time.Second)
		commandErr := a.Commands.Close(commandCtx)
		commandCancel()
		if a.CommandLogs != nil {
			_ = a.CommandLogs.Close()
		}
		if a.Input != nil {
			a.Input.Close()
		}
		a.closeErr = errors.Join(chatErr, commandErr, a.Store.Close())
	})
	return a.closeErr
}
func (a *App) Address() string       { return a.webAddress }
func (a *App) PeerAddress() string   { return a.peerAddress }
func (a *App) Done() <-chan struct{} { return a.done }

func (a *App) notifyRefresh() {
	a.refreshMu.Lock()
	if a.refreshCh != nil {
		close(a.refreshCh)
	}
	a.refreshCh = make(chan struct{})
	a.refreshMu.Unlock()
}

func (a *App) refreshDone() <-chan struct{} {
	a.refreshMu.Lock()
	if a.refreshCh == nil {
		a.refreshCh = make(chan struct{})
	}
	ch := a.refreshCh
	a.refreshMu.Unlock()
	return ch
}
