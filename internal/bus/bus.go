// Package bus 定义平台、网关和 Agent 之间传递的统一消息。
package bus

// ChannelAccount 唯一标识某个平台下的一个机器人账号实例。
// ChannelManager 使用它选择负责发送消息的具体渠道适配器。
type ChannelAccount struct {
	Channel   string
	AccountID string
}

// ConversationAddress 唯一标识一个机器人账号下的聊天或线程。
// ThreadID 为空时表示基础聊天，非空时表示该聊天中的独立线程或话题。
type ConversationAddress struct {
	Channel   string
	AccountID string
	ChatID    string
	ThreadID  string
}

// Account 返回当前会话所属的平台账号地址。
func (a ConversationAddress) Account() ChannelAccount {
	return ChannelAccount{Channel: a.Channel, AccountID: a.AccountID}
}

// InboundMessage 表示任意平台转换后的入站消息。
type InboundMessage struct {
	// 平台类型
	Channel string

	// 接收消息的机器人账号
	AccountID string

	// 消息所在的私聊或群聊
	ChatID string

	// 消息所在的线程或话题；为空表示基础聊天
	ThreadID string

	// 实际发消息的用户
	UserID string

	// 平台上的某一条具体消息
	MessageID string

	// 统一后的文本内容
	Text string

	// AgentID 明确指定本条消息要交给哪个 Agent；为空时使用渠道绑定。
	AgentID string

	// ModelRef 仅覆盖本条消息使用的模型，不修改会话模型。
	ModelRef string
}

// Address 返回用于路由和会话隔离的统一地址。
func (m InboundMessage) Address() ConversationAddress {
	return ConversationAddress{
		Channel:   m.Channel,
		AccountID: m.AccountID,
		ChatID:    m.ChatID,
		ThreadID:  m.ThreadID,
	}
}

// OutboundMessage 表示 Agent 处理完成后需要发回平台的消息。
type OutboundMessage struct {
	Channel      string
	AccountID    string
	ChatID       string
	ThreadID     string
	Text         string
	ReplyToMsgID string
}

// Address 返回出站消息需要投递到的统一会话地址。
func (m OutboundMessage) Address() ConversationAddress {
	return ConversationAddress{
		Channel:   m.Channel,
		AccountID: m.AccountID,
		ChatID:    m.ChatID,
		ThreadID:  m.ThreadID,
	}
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
