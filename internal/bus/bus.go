// Package bus 定义平台、网关和 Agent 之间传递的统一消息。
package bus

// InboundMessage 表示任意平台转换后的入站消息。
type InboundMessage struct {
	Channel   string
	AccountID string
	ChatID    string
	UserID    string
	MessageID string
	Text      string
}

// SessionTriple 返回用于隔离会话的平台、机器人账号和聊天标识。
func (m InboundMessage) SessionTriple() (channel, accountID, chatID string) {
	return m.Channel, m.AccountID, m.ChatID
}

// OutboundMessage 表示 Agent 处理完成后需要发回平台的消息。
type OutboundMessage struct {
	Channel      string
	AccountID    string
	ChatID       string
	Text         string
	ReplyToMsgID string
}

// MessageBus 是进程内的异步消息总线。
type MessageBus struct {
	Inbound  chan InboundMessage
	Outbound chan OutboundMessage
}

// New 创建带缓冲区的消息总线，避免平台回调短暂阻塞。
func New() *MessageBus {
	return &MessageBus{
		Inbound:  make(chan InboundMessage, 100),
		Outbound: make(chan OutboundMessage, 100),
	}
}
