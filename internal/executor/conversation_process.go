package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/prompt"
)

type chatCommand struct {
	Prompt         *prompt.Application  `json:"prompt,omitempty"`
	PromptInstance string               `json:"prompt_instance,omitempty"`
	ID             string               `json:"id"`
	Action         string               `json:"action"`
	Binding        conversation.Binding `json:"binding"`
	Run            string               `json:"run,omitempty"`
	Text           string               `json:"text,omitempty"`
	Message        string               `json:"message,omitempty"`
	Offset         int                  `json:"offset,omitempty"`
	Cursor         string               `json:"cursor,omitempty"`
	Started        int64                `json:"started,omitempty"`
	Entry          string               `json:"entry,omitempty"`
	Port           int                  `json:"port,omitempty"`
	Token          string               `json:"token,omitempty"`
}

type chatResult struct {
	ID     string              `json:"id"`
	Result json.RawMessage     `json:"result"`
	Error  *conversation.Error `json:"error"`
}

type chatProcess struct {
	input    io.WriteCloser
	output   io.ReadCloser
	cleanup  func()
	done     chan struct{}
	exited   chan struct{}
	mu       sync.Mutex
	write    sync.Mutex
	sequence uint64
	pending  map[string]chan chatResult
	finish   sync.Once
}

func launchChatProcess(cmd *exec.Cmd) (*chatProcess, error) {
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, err
	}
	// Gateway diagnostics are private and may contain prompt/provider content.
	// The bridge emits only bounded public results; stderr is never persisted.
	cmd.Stderr = io.Discard
	cleanup, err := startManaged(cmd)
	if err != nil {
		input.Close()
		output.Close()
		return nil, err
	}
	p := &chatProcess{input: input, output: output, cleanup: cleanup, done: make(chan struct{}), exited: make(chan struct{}), pending: map[string]chan chatResult{}}
	go p.read()
	go func() { cmd.Wait(); close(p.exited); p.stop() }()
	return p, nil
}

func (p *chatProcess) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *chatProcess) stop() {
	p.finish.Do(func() {
		close(p.done)
		p.input.Close()
		p.output.Close()
		p.cleanup()
	})
}

func (p *chatProcess) close() error {
	p.stop()
	select {
	case <-p.exited:
		return nil
	case <-time.After(5 * time.Second):
		return conversation.Fail("cleanup_pending", "聊天运行时退出尚未确认")
	}
}

func (p *chatProcess) read() {
	defer p.stop()
	scanner := bufio.NewScanner(p.output)
	scanner.Buffer(make([]byte, 8192), 128*1024)
	for scanner.Scan() {
		var result chatResult
		if json.Unmarshal(scanner.Bytes(), &result) != nil || result.ID == "" {
			return
		}
		p.mu.Lock()
		waiter := p.pending[result.ID]
		delete(p.pending, result.ID)
		p.mu.Unlock()
		if waiter != nil {
			waiter <- result
		}
	}
}

func (p *chatProcess) call(ctx context.Context, command chatCommand, result any) error {
	p.mu.Lock()
	if !p.alive() || len(p.pending) >= 16 {
		p.mu.Unlock()
		return conversation.Fail("unavailable", "聊天运行时连接不可用，请核对操作状态")
	}
	p.sequence++
	command.ID = strconv.FormatUint(p.sequence, 10)
	waiter := make(chan chatResult, 1)
	p.pending[command.ID] = waiter
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pending, command.ID); p.mu.Unlock() }()
	data, err := json.Marshal(command)
	if err != nil || len(data) >= 128*1024 {
		return conversation.Fail("invalid_request", "聊天请求超过传输限制")
	}
	err = p.writeCommand(ctx, append(data, '\n'))
	if err != nil {
		p.stop()
		return conversation.Fail("unavailable", "聊天连接中断，请核对操作状态")
	}
	select {
	case reply := <-waiter:
		if reply.Error != nil {
			return reply.Error
		}
		if result != nil {
			if err := json.Unmarshal(reply.Result, result); err != nil {
				return fmt.Errorf("invalid conversation runtime result")
			}
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return conversation.Fail("unavailable", "聊天运行时中断，请核对操作状态")
	}
}

func (p *chatProcess) writeCommand(ctx context.Context, data []byte) error {
	done := make(chan error, 1)
	go func() {
		p.write.Lock()
		defer p.write.Unlock()
		_, err := p.input.Write(data)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		p.stop()
		<-done
		return ctx.Err()
	case <-p.done:
		<-done
		return conversation.Fail("unavailable", "聊天连接已关闭")
	}
}
