package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	defaultRequestTimeout = 30 * time.Second
	defaultMaxResultBytes = 1 << 20
	maxStdioMessageBytes  = 4 << 20
	closeGracePeriod      = 2 * time.Second
)

// StdioClient 通过换行分隔的 JSON-RPC 消息连接 MCP 子进程。
type StdioClient struct {
	options StdioOptions

	mu          sync.Mutex
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	started     bool
	closed      bool
	pending     map[int64]chan responseOrError
	done        chan struct{}
	processDone chan error
	closeOnce   sync.Once

	writeMu sync.Mutex
	nextID  atomic.Int64
}

// NewStdioClient 创建尚未启动的 stdio MCP 客户端。
func NewStdioClient(options StdioOptions) (*StdioClient, error) {
	options.Command = strings.TrimSpace(options.Command)
	if options.Command == "" {
		return nil, fmt.Errorf("MCP stdio command cannot be empty")
	}
	if options.RequestTimeout == 0 {
		options.RequestTimeout = defaultRequestTimeout
	}
	if options.RequestTimeout < 0 {
		return nil, fmt.Errorf("MCP request timeout cannot be negative")
	}
	if options.MaxResultBytes == 0 {
		options.MaxResultBytes = defaultMaxResultBytes
	}
	if options.MaxResultBytes < 1 {
		return nil, fmt.Errorf("MCP max result bytes must be greater than zero")
	}
	return &StdioClient{
		options:     options,
		pending:     make(map[int64]chan responseOrError),
		done:        make(chan struct{}),
		processDone: make(chan error, 1),
	}, nil
}

// Connect 启动子进程并完成 MCP 初始化握手。
func (c *StdioClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return fmt.Errorf("MCP stdio client is already started")
	}
	cmd := exec.Command(c.options.Command, c.options.Args...)
	cmd.Env = append([]string(nil), os.Environ()...)
	keys := make([]string, 0, len(c.options.Env))
	for key := range c.options.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		cmd.Env = append(cmd.Env, key+"="+c.options.Env[key])
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		c.mu.Unlock()
		return fmt.Errorf("create MCP stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.mu.Unlock()
		_ = stdin.Close()
		return fmt.Errorf("create MCP stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		c.mu.Unlock()
		_ = stdin.Close()
		return fmt.Errorf("create MCP stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		c.mu.Unlock()
		_ = stdin.Close()
		return fmt.Errorf("start MCP command %q: %w", c.options.Command, err)
	}
	c.cmd = cmd
	c.stdin = stdin
	c.started = true
	c.mu.Unlock()

	go c.readResponses(stdout)
	go c.readStderr(stderr)
	go func() { c.processDone <- cmd.Wait() }()

	var initialized struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
	}
	if err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": latestProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]string{
			"name":    "luckyclaw",
			"version": "0.1.0",
		},
	}, &initialized); err != nil {
		_ = c.Close(context.Background())
		return fmt.Errorf("initialize MCP server: %w", err)
	}
	if _, supported := supportedProtocolVersions[initialized.ProtocolVersion]; !supported {
		_ = c.Close(context.Background())
		return fmt.Errorf("MCP server selected unsupported protocol version %q", initialized.ProtocolVersion)
	}
	if _, tools := initialized.Capabilities["tools"]; !tools {
		_ = c.Close(context.Background())
		return fmt.Errorf("MCP server does not declare tools capability")
	}
	if err := c.notify("notifications/initialized", nil); err != nil {
		_ = c.Close(context.Background())
		return fmt.Errorf("notify MCP initialized: %w", err)
	}
	return nil
}

// ListTools 读取所有分页工具并保持服务端顺序。
func (c *StdioClient) ListTools(ctx context.Context) ([]ToolDefinition, error) {
	var result []ToolDefinition
	cursor := ""
	seen := make(map[string]struct{})
	for page := 0; page < 100; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var response struct {
			Tools      []ToolDefinition `json:"tools"`
			NextCursor string           `json:"nextCursor"`
		}
		if err := c.request(ctx, "tools/list", params, &response); err != nil {
			return nil, fmt.Errorf("list MCP tools: %w", err)
		}
		result = append(result, response.Tools...)
		if response.NextCursor == "" {
			return result, nil
		}
		if _, repeated := seen[response.NextCursor]; repeated {
			return nil, fmt.Errorf("MCP tools/list repeated cursor %q", response.NextCursor)
		}
		seen[response.NextCursor] = struct{}{}
		cursor = response.NextCursor
	}
	return nil, fmt.Errorf("MCP tools/list exceeded 100 pages")
}

// CallTool 调用一个 MCP 工具并保留结构化与非文本内容。
func (c *StdioClient) CallTool(ctx context.Context, name string, arguments json.RawMessage) (ToolResult, error) {
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	var decoded map[string]any
	if err := json.Unmarshal(arguments, &decoded); err != nil {
		return ToolResult{}, fmt.Errorf("decode MCP tool arguments: %w", err)
	}
	var result ToolResult
	if err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": decoded}, &result); err != nil {
		return ToolResult{}, fmt.Errorf("call MCP tool %q: %w", name, err)
	}
	return result, nil
}

func (c *StdioClient) request(parent context.Context, method string, params, target any) error {
	ctx, cancel := context.WithTimeout(parent, c.options.RequestTimeout)
	defer cancel()
	id := c.nextID.Add(1)
	responseChannel := make(chan responseOrError, 1)
	c.mu.Lock()
	if !c.started || c.closed {
		c.mu.Unlock()
		return fmt.Errorf("MCP stdio client is not connected")
	}
	c.pending[id] = responseChannel
	c.mu.Unlock()

	if err := c.writeJSON(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		c.removePending(id)
		return err
	}
	select {
	case response := <-responseChannel:
		if response.err != nil {
			return response.err
		}
		if response.response.Error != nil {
			return fmt.Errorf("JSON-RPC error %d: %s", response.response.Error.Code, response.response.Error.Message)
		}
		if len(response.response.Result) > c.options.MaxResultBytes {
			return fmt.Errorf("MCP response exceeds %d bytes", c.options.MaxResultBytes)
		}
		if target == nil {
			return nil
		}
		if err := json.Unmarshal(response.response.Result, target); err != nil {
			return fmt.Errorf("decode MCP %s response: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		c.removePending(id)
		_ = c.notify("notifications/cancelled", map[string]any{"requestId": id, "reason": ctx.Err().Error()})
		return ctx.Err()
	case <-c.done:
		c.removePending(id)
		return fmt.Errorf("MCP stdio process stopped")
	}
}

func (c *StdioClient) notify(method string, params any) error {
	return c.writeJSON(rpcNotification{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *StdioClient) writeJSON(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode MCP message: %w", err)
	}
	data = append(data, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	stdin := c.stdin
	closed := c.closed
	c.mu.Unlock()
	if stdin == nil || closed {
		return fmt.Errorf("MCP stdio client is closed")
	}
	if _, err := stdin.Write(data); err != nil {
		return fmt.Errorf("write MCP message: %w", err)
	}
	return nil
}

func (c *StdioClient) readResponses(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxStdioMessageBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		var response rpcResponse
		if err := json.Unmarshal(line, &response); err != nil {
			c.fail(fmt.Errorf("decode MCP stdout: %w", err))
			return
		}
		if len(response.ID) == 0 || string(response.ID) == "null" {
			continue
		}
		var id int64
		if err := json.Unmarshal(response.ID, &id); err != nil {
			continue
		}
		c.mu.Lock()
		channel := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if channel != nil {
			channel <- responseOrError{response: response}
		}
	}
	if err := scanner.Err(); err != nil {
		c.fail(fmt.Errorf("read MCP stdout: %w", err))
		return
	}
	c.fail(io.EOF)
}

func (c *StdioClient) readStderr(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 16<<10), 256<<10)
	for scanner.Scan() {
		log.Printf("MCP[%s] %s", c.options.Command, scanner.Text())
	}
}

func (c *StdioClient) removePending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *StdioClient) fail(err error) {
	c.closeOnce.Do(func() {
		close(c.done)
		c.mu.Lock()
		pending := c.pending
		c.pending = make(map[int64]chan responseOrError)
		c.mu.Unlock()
		for _, channel := range pending {
			channel <- responseOrError{err: err}
		}
	})
}

// Close 按关闭 stdin、SIGTERM、SIGKILL 的顺序终止 MCP 子进程。
func (c *StdioClient) Close(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	stdin := c.stdin
	cmd := c.cmd
	c.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd == nil || cmd.Process == nil {
		c.fail(errors.New("MCP client closed"))
		return nil
	}
	if c.waitForProcess(ctx, closeGracePeriod) {
		c.fail(errors.New("MCP client closed"))
		return nil
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	if c.waitForProcess(ctx, closeGracePeriod) {
		c.fail(errors.New("MCP client closed"))
		return nil
	}
	_ = cmd.Process.Kill()
	if !c.waitForProcess(ctx, closeGracePeriod) {
		return fmt.Errorf("MCP process did not exit after SIGKILL")
	}
	c.fail(errors.New("MCP client closed"))
	return nil
}

func (c *StdioClient) waitForProcess(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-c.processDone:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

var _ Client = (*StdioClient)(nil)
