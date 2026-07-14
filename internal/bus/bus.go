// Package bus 定义平台、网关和 Agent 之间传递的统一消息。
package bus

// InboundMessage 表示任意平台转换后的入站消息。
type InboundMessage struct {
	//平台类型
	Channel string

	//接收消息的机器人账号
	AccountID string

	//消息所在的私聊或群聊
	ChatID string

	//实际发消息的用户
	UserID string

	//平台上的某一条具体消息
	MessageID string

	//统一后的文本内容
	Text string

	// AgentID 明确指定本条消息要交给哪个 Agent；为空时使用渠道绑定。
	AgentID string

	// ModelRef 仅覆盖本条消息使用的模型，不修改会话模型。
	ModelRef string
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
