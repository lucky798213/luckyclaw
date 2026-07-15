package gateway

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lukcyclaw/internal/bus"
)

func inboundMessage(address bus.ConversationAddress, messageID string) bus.InboundMessage {
	return bus.InboundMessage{
		Channel:   address.Channel,
		AccountID: address.AccountID,
		ChatID:    address.ChatID,
		ThreadID:  address.ThreadID,
		MessageID: messageID,
	}
}

func TestMessageDeduperAllowsOnlyFirstMessage(t *testing.T) {
	deduper := newMessageDeduper(defaultDedupTTL)
	msg := inboundMessage(bus.ConversationAddress{
		Channel:   "feishu",
		AccountID: "bot",
		ChatID:    "chat",
		ThreadID:  "topic",
	}, "message-1")

	if deduper.isDuplicate(msg) {
		t.Fatal("首次消息被错误识别为重复消息")
	}
	if !deduper.isDuplicate(msg) {
		t.Fatal("相同地址和消息 ID 没有被识别为重复消息")
	}
}

func TestMessageDeduperForgetAllowsRetry(t *testing.T) {
	deduper := newMessageDeduper(defaultDedupTTL)
	msg := inboundMessage(bus.ConversationAddress{Channel: "feishu", AccountID: "bot", ChatID: "chat"}, "message-1")

	if deduper.isDuplicate(msg) {
		t.Fatal("首次消息被错误识别为重复消息")
	}
	deduper.forget(msg)
	if deduper.isDuplicate(msg) {
		t.Fatal("撤销登记后的消息没有被重新允许")
	}
}

func TestMessageDeduperUsesFullConversationAddress(t *testing.T) {
	deduper := newMessageDeduper(defaultDedupTTL)
	messageID := "shared-message"
	messages := []bus.InboundMessage{
		inboundMessage(bus.ConversationAddress{Channel: "feishu", AccountID: "bot", ChatID: "chat", ThreadID: "topic"}, messageID),
		inboundMessage(bus.ConversationAddress{Channel: "telegram", AccountID: "bot", ChatID: "chat", ThreadID: "topic"}, messageID),
		inboundMessage(bus.ConversationAddress{Channel: "feishu", AccountID: "other-bot", ChatID: "chat", ThreadID: "topic"}, messageID),
		inboundMessage(bus.ConversationAddress{Channel: "feishu", AccountID: "bot", ChatID: "other-chat", ThreadID: "topic"}, messageID),
		inboundMessage(bus.ConversationAddress{Channel: "feishu", AccountID: "bot", ChatID: "chat", ThreadID: "other-topic"}, messageID),
	}

	for _, msg := range messages {
		if deduper.isDuplicate(msg) {
			t.Fatalf("不同完整地址的消息被错误去重: %+v", msg.Address())
		}
	}
}

func TestMessageDeduperSkipsEmptyMessageID(t *testing.T) {
	deduper := newMessageDeduper(defaultDedupTTL)
	msg := inboundMessage(bus.ConversationAddress{Channel: "terminal", AccountID: "local", ChatID: "default"}, "")

	if deduper.isDuplicate(msg) || deduper.isDuplicate(msg) {
		t.Fatal("空消息 ID 不应该参与去重")
	}
	if len(deduper.entries) != 0 {
		t.Fatalf("空消息 ID 被写入缓存: %d", len(deduper.entries))
	}
}

func TestMessageDeduperAllowsExpiredMessage(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	deduper := newMessageDeduper(defaultDedupTTL)
	deduper.now = func() time.Time { return current }
	msg := inboundMessage(bus.ConversationAddress{Channel: "feishu", AccountID: "bot", ChatID: "chat"}, "message-1")

	if deduper.isDuplicate(msg) {
		t.Fatal("首次消息被错误识别为重复消息")
	}
	current = current.Add(59 * time.Second)
	if !deduper.isDuplicate(msg) {
		t.Fatal("去重窗口内的消息没有被拦截")
	}
	current = current.Add(time.Second)
	if deduper.isDuplicate(msg) {
		t.Fatal("达到过期时间的消息没有被重新允许")
	}
	if !deduper.isDuplicate(msg) {
		t.Fatal("重新建立窗口后重复消息没有被拦截")
	}
}

func TestMessageDeduperCleanup(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	deduper := newMessageDeduper(defaultDedupTTL)
	deduper.now = func() time.Time { return current }
	expired := inboundMessage(bus.ConversationAddress{Channel: "feishu", AccountID: "bot", ChatID: "expired"}, "message-1")
	active := inboundMessage(bus.ConversationAddress{Channel: "feishu", AccountID: "bot", ChatID: "active"}, "message-2")

	deduper.isDuplicate(expired)
	current = current.Add(30 * time.Second)
	deduper.isDuplicate(active)
	current = current.Add(30 * time.Second)
	deduper.cleanup()

	if len(deduper.entries) != 1 {
		t.Fatalf("清理后缓存数量 = %d，期望 1", len(deduper.entries))
	}
	activeKey := messageDedupKey{Address: active.Address(), MessageID: active.MessageID}
	if _, exists := deduper.entries[activeKey]; !exists {
		t.Fatal("清理函数错误删除了未过期记录")
	}
}

func TestMessageDeduperConcurrentAccess(t *testing.T) {
	deduper := newMessageDeduper(defaultDedupTTL)
	msg := inboundMessage(bus.ConversationAddress{Channel: "telegram", AccountID: "bot", ChatID: "chat"}, "message-1")
	const goroutines = 100
	var firstCount atomic.Int32
	var waitGroup sync.WaitGroup

	waitGroup.Add(goroutines)
	for range goroutines {
		go func() {
			defer waitGroup.Done()
			if !deduper.isDuplicate(msg) {
				firstCount.Add(1)
			}
		}()
	}
	waitGroup.Wait()

	if got := firstCount.Load(); got != 1 {
		t.Fatalf("并发首次消息数量 = %d，期望 1", got)
	}
}
