package channels

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lucky798213/luckyclaw/internal/bus"
)

const (
	terminalChannelName = "terminal"
	terminalAccountID   = "local"
	terminalChatID      = "default"
	terminalUserID      = "local-user"
)

// TerminalChannel 把标准输入输出适配成一个平台渠道，供最小链路验证使用。
type TerminalChannel struct {
	reader  io.Reader
	writer  io.Writer
	bus     *bus.MessageBus
	done    chan struct{}
	doneOne sync.Once
	writeMu sync.Mutex
	seq     atomic.Uint64
}

// NewTerminal 创建终端渠道。
func NewTerminal(reader io.Reader, writer io.Writer, messageBus *bus.MessageBus) (*TerminalChannel, error) {
	if reader == nil || writer == nil {
		return nil, fmt.Errorf("terminal reader and writer cannot be nil")
	}
	if messageBus == nil {
		return nil, fmt.Errorf("message bus cannot be nil")
	}
	return &TerminalChannel{
		reader: reader,
		writer: writer,
		bus:    messageBus,
		done:   make(chan struct{}),
	}, nil
}

// Name 返回终端平台名称。
func (t *TerminalChannel) Name() string { return terminalChannelName }

// AccountID 返回本地终端账号标识。
func (t *TerminalChannel) AccountID() string { return terminalAccountID }

// Done 在标准输入结束时关闭，供主程序安全退出。
func (t *TerminalChannel) Done() <-chan struct{} { return t.done }

// Start 读取终端文本并转换为统一入站消息。
func (t *TerminalChannel) Start(ctx context.Context) error {
	defer t.doneOne.Do(func() { close(t.done) })
	t.write("输入 /new 开始新会话，按 Ctrl+D 或 Ctrl+C 退出\n> ")

	scanner := bufio.NewScanner(t.reader)
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			t.write("> ")
			continue
		}
		msg := bus.InboundMessage{
			Channel:   terminalChannelName,
			AccountID: terminalAccountID,
			ChatID:    terminalChatID,
			UserID:    terminalUserID,
			MessageID: fmt.Sprintf("terminal-%d", t.seq.Add(1)),
			Text:      text,
		}
		select {
		case t.bus.Inbound <- msg:
		case <-ctx.Done():
			return nil
		}
	}
	return scanner.Err()
}

// SendMessage 把统一出站消息打印到终端。
func (t *TerminalChannel) SendMessage(msg bus.OutboundMessage) error {
	t.write(msg.Text + "\n> ")
	return nil
}

func (t *TerminalChannel) write(text string) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_, _ = io.WriteString(t.writer, text)
}
