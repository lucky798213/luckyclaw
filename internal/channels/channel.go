// Package channels 定义平台适配器及其出站分发逻辑。
package channels

import (
	"context"

	"github.com/lucky798213/luckyclaw/internal/bus"
)

// Channel 是所有平台适配器需要实现的最小接口。
type Channel interface {
	// Name 返回平台类型，例如 terminal、feishu 或 wechat。
	Name() string
	// AccountID 返回该平台下的机器人账号标识。
	AccountID() string
	// Start 开始接收平台消息，并统一写入消息总线。
	Start(ctx context.Context) error
	// SendMessage 把统一出站消息转换为平台请求并发送。
	SendMessage(msg bus.OutboundMessage) error
}
