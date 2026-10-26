package quickinput

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

// remoteSurface owns one host generation. It never respawns itself; Controller
// serializes replacement with settings transactions and registration lifetime.
type remoteSurface struct {
	process                                 hostProcess
	draft                                   *Draft
	session                                 string
	epoch                                   uint64
	out                                     chan hostipc.Message
	done                                    chan struct{}
	ready                                   chan error
	finishOnce                              sync.Once
	closeOnce                               sync.Once
	mu                                      sync.Mutex
	closing, initialized                    bool
	err, closeErr                           error
	runtime                                 string
	request, lastSequence, acceptedRevision uint64
	lastText                                string
	waiters                                 map[uint64]chan hostipc.Message
	onFailure                               func(error)
}

func startRemoteSurface(home, theme string, draft *Draft, launch hostLauncher, failed func(error), recovered ...bool) (*remoteSurface, error) {
	process, err := launch()
	if err != nil {
		return nil, err
	}
	text, revision, epoch := draft.state()
	s := &remoteSurface{process: process, draft: draft, session: model.ID(), epoch: epoch,
		out: make(chan hostipc.Message, 32), done: make(chan struct{}), ready: make(chan error, 1),
		waiters: map[uint64]chan hostipc.Message{}, onFailure: failed, lastSequence: revision, lastText: text, acceptedRevision: revision}
	go s.writeLoop()
	go s.readLoop()
	go func() { <-process.Exited(); s.finish(hostFailure("host_exited")) }()
	notice := len(recovered) != 0 && recovered[0]
	err = s.send(hostipc.Message{Kind: hostipc.Initialize, Home: home, Theme: theme, Text: text, Revision: revision, Epoch: epoch, AppVersion: config.Version, Source: config.SourceDigest, Recovered: notice})
	if err == nil {
		select {
		case err = <-s.ready:
		case <-s.done:
			err = s.failure()
		case <-time.After(hostStartTimeout):
			err = hostFailure("host_start_timeout")
		}
	}
	if err != nil {
		if closeErr := s.Close(); closeErr != nil {
			return s, fmt.Errorf("%v；%w", err, closeErr)
		}
		return nil, err
	}
	return s, nil
}

func (s *remoteSurface) send(m hostipc.Message) error {
	m.Version, m.Session = hostipc.Version, s.session
	select {
	case <-s.done:
		return s.failure()
	default:
	}
	select {
	case s.out <- m:
		return nil
	case <-s.done:
		return s.failure()
	default:
		s.finish(hostFailure("host_queue_full"))
		return hostFailure("host_queue_full")
	}
}

func (s *remoteSurface) writeLoop() {
	for {
		select {
		case m := <-s.out:
			if err := hostipc.Write(s.process.Input(), m, false); err != nil {
				s.finish(hostFailure("host_disconnected"))
				return
			}
		case <-s.done:
			return
		}
	}
}

func (s *remoteSurface) readLoop() {
	for {
		m, err := hostipc.Read(s.process.Output(), true)
		if err != nil {
			s.finish(hostFailure("host_disconnected"))
			return
		}
		if m.Session != s.session {
			s.finish(hostFailure("host_protocol_invalid"))
			return
		}
		if err := s.receive(m); err != nil {
			s.finish(err)
			return
		}
	}
}

func (s *remoteSurface) receive(m hostipc.Message) error {
	if m.Kind == hostipc.Ready {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.initialized || m.AppVersion != config.Version || m.Source != config.SourceDigest {
			return hostFailure("host_protocol_invalid")
		}
		s.initialized, s.runtime = true, m.Runtime
		s.ready <- nil
		return nil
	}
	if m.Kind == hostipc.Failure {
		err := hostFailure(m.Code)
		if m.Code == "focus_denied" {
			if s.onFailure != nil {
				s.onFailure(err)
			}
			return nil
		}
		return err
	}
	s.mu.Lock()
	initialized := s.initialized
	s.mu.Unlock()
	if !initialized {
		return hostFailure("host_protocol_invalid")
	}
	switch m.Kind {
	case hostipc.Edit:
		return s.acceptEdit(m)
	case hostipc.SnapshotDone, hostipc.State:
		s.mu.Lock()
		waiter := s.waiters[m.Request]
		if m.Kind == hostipc.SnapshotDone && (m.Sequence > s.lastSequence || m.Epoch != s.epoch || m.Revision > s.acceptedRevision) {
			s.mu.Unlock()
			return hostFailure("host_protocol_invalid")
		}
		delete(s.waiters, m.Request)
		s.mu.Unlock()
		if waiter != nil {
			waiter <- m
		}
		return nil
	default:
		return hostFailure("host_protocol_invalid")
	}
}

func (s *remoteSurface) acceptEdit(m hostipc.Message) error {
	s.mu.Lock()
	// A frame may have been read before Close/finish retired this session. The
	// shared draft can already belong to its replacement, even with the same epoch.
	if s.closing || s.err != nil {
		s.mu.Unlock()
		return hostFailure("host_disconnected")
	}
	if m.Epoch != s.epoch || m.Sequence < s.lastSequence || (m.Sequence == s.lastSequence && m.Text != s.lastText) {
		s.mu.Unlock()
		return hostFailure("host_protocol_invalid")
	}
	revision, err := s.draft.accept(m.Text, m.Epoch)
	if err == nil {
		s.lastSequence, s.lastText, s.acceptedRevision = m.Sequence, m.Text, revision
	}
	s.mu.Unlock()
	if err != nil {
		return hostFailure("host_protocol_invalid")
	}
	return s.send(hostipc.Message{Kind: hostipc.Accepted, Sequence: m.Sequence, Epoch: m.Epoch, Revision: revision})
}

func (s *remoteSurface) finish(err error) {
	s.finishOnce.Do(func() {
		s.mu.Lock()
		s.err = err
		notify := s.initialized && !s.closing
		s.mu.Unlock()
		close(s.done)
		_ = s.process.Input().Close()
		_ = s.process.Output().Close()
		if notify && s.onFailure != nil {
			s.onFailure(err)
		}
	})
}

func (s *remoteSurface) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	return hostFailure("host_disconnected")
}

func (s *remoteSurface) Toggle() {
	if err := s.process.GrantForeground(); err != nil {
		if s.onFailure != nil {
			s.onFailure(err)
		}
		return
	}
	s.mu.Lock()
	s.request++
	id := s.request
	s.mu.Unlock()
	_ = s.send(hostipc.Message{Kind: hostipc.Toggle, Request: id})
}

func (s *remoteSurface) call(ctx context.Context, m hostipc.Message, expected hostipc.Kind) error {
	s.mu.Lock()
	if s.closing || len(s.waiters) >= 8 {
		s.mu.Unlock()
		return fmt.Errorf("输入界面正在处理其他操作")
	}
	s.request++
	m.Request = s.request
	waiter := make(chan hostipc.Message, 1)
	s.waiters[m.Request] = waiter
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.waiters, m.Request); s.mu.Unlock() }()
	if err := s.send(m); err != nil {
		return err
	}
	select {
	case result := <-waiter:
		if result.Kind != expected {
			return hostFailure("host_protocol_invalid")
		}
		if result.Code != "" {
			return hostFailure(result.Code)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return s.failure()
	}
}

func (s *remoteSurface) Update(theme string) error {
	if theme != "star" && theme != "frost" {
		return fmt.Errorf("主题无效")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return s.call(ctx, hostipc.Message{Kind: hostipc.Theme, Theme: theme}, hostipc.State)
}

func (s *remoteSurface) Snapshot(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return s.call(ctx, hostipc.Message{Kind: hostipc.Snapshot}, hostipc.SnapshotDone)
}

func (s *remoteSurface) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		if s.send(hostipc.Message{Kind: hostipc.Quit}) == nil {
			select {
			case <-s.process.Exited():
			case <-s.done:
			case <-time.After(hostStopTimeout):
			}
		}
		s.finish(hostFailure("host_disconnected"))
		s.closeErr = s.process.Stop()
	})
	return s.closeErr
}

func hostFailure(code string) error {
	details := map[string]string{
		"focus_denied":                  "系统拒绝激活输入框，请从托盘重试",
		"host_exited":                   "输入宿主已退出",
		"host_disconnected":             "输入宿主通信中断",
		"host_start_timeout":            "输入宿主初始化超时，请重试",
		"host_queue_full":               "输入宿主消息队列已满",
		"host_protocol_invalid":         "输入宿主协议校验失败",
		"runtime_unavailable":           "WebView2 Runtime 不可用，请运行安装器补齐",
		"runtime_override":              "WebView2 存在外部启动参数或目录策略覆盖，快捷输入已停用；请检查 WebView2 策略",
		"runtime_initialization_failed": "WebView2 初始化失败，请重试并查看运行时诊断",
		"renderer_failed":               "WebView2 渲染进程中断",
		"snapshot_failed":               "请先完成输入法候选，再重试",
	}
	detail, ok := details[code]
	if !ok {
		code, detail = "host_protocol_invalid", details["host_protocol_invalid"]
	}
	return &RegistrationError{code, detail}
}
