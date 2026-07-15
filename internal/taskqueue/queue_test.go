package taskqueue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lucky798213/luckyclaw/internal/bus"
)

func testMessage(address bus.ConversationAddress, text string) bus.InboundMessage {
	return bus.InboundMessage{
		Channel:   address.Channel,
		AccountID: address.AccountID,
		ChatID:    address.ChatID,
		ThreadID:  address.ThreadID,
		MessageID: text,
		Text:      text,
	}
}

func receiveWithin[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(time.Second):
		t.Fatal("等待任务队列事件超时")
		var zero T
		return zero
	}
}

func TestNewQueueValidatesOptions(t *testing.T) {
	handler := func(context.Context, bus.InboundMessage) {}
	tests := []struct {
		name          string
		maxConcurrent int
		taskTimeout   time.Duration
		maxPending    int
		handler       TaskHandler
	}{
		{name: "并发数无效", maxConcurrent: 0, taskTimeout: time.Second, maxPending: 1, handler: handler},
		{name: "超时时间无效", maxConcurrent: 1, taskTimeout: 0, maxPending: 1, handler: handler},
		{name: "等待上限无效", maxConcurrent: 1, taskTimeout: time.Second, maxPending: 0, handler: handler},
		{name: "处理器为空", maxConcurrent: 1, taskTimeout: time.Second, maxPending: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewQueue(test.maxConcurrent, test.taskTimeout, test.maxPending, test.handler); err == nil {
				t.Fatal("NewQueue() error = nil")
			}
		})
	}
}

func TestQueueSerializesSameConversationInFIFOOrder(t *testing.T) {
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	done := make(chan string, 2)
	var running atomic.Int32
	var maxRunning atomic.Int32

	queue, err := NewQueue(10, time.Second, 10, func(_ context.Context, msg bus.InboundMessage) {
		current := running.Add(1)
		for {
			maximum := maxRunning.Load()
			if current <= maximum || maxRunning.CompareAndSwap(maximum, current) {
				break
			}
		}
		if msg.Text == "first" {
			close(firstStarted)
			<-releaseFirst
		} else {
			close(secondStarted)
		}
		done <- msg.Text
		running.Add(-1)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Stop()

	address := bus.ConversationAddress{Channel: "feishu", AccountID: "bot", ChatID: "chat", ThreadID: "topic"}
	if err := queue.Submit(testMessage(address, "first")); err != nil {
		t.Fatal(err)
	}
	receiveWithin(t, firstStarted)
	if err := queue.Submit(testMessage(address, "second")); err != nil {
		t.Fatal(err)
	}

	select {
	case <-secondStarted:
		t.Fatal("同一会话的第二条消息在第一条完成前开始执行")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	if got := receiveWithin(t, done); got != "first" {
		t.Fatalf("第一条完成消息 = %q", got)
	}
	if got := receiveWithin(t, done); got != "second" {
		t.Fatalf("第二条完成消息 = %q", got)
	}
	if got := maxRunning.Load(); got != 1 {
		t.Fatalf("同一会话最大并发数 = %d，期望 1", got)
	}
}

func TestQueueRunsDifferentThreadsConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	done := make(chan struct{}, 2)
	queue, err := NewQueue(2, time.Second, 10, func(_ context.Context, msg bus.InboundMessage) {
		started <- msg.ThreadID
		<-release
		done <- struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Stop()

	base := bus.ConversationAddress{Channel: "feishu", AccountID: "bot", ChatID: "chat"}
	first := base
	first.ThreadID = "topic-1"
	second := base
	second.ThreadID = "topic-2"
	if err := queue.Submit(testMessage(first, "first")); err != nil {
		t.Fatal(err)
	}
	if err := queue.Submit(testMessage(second, "second")); err != nil {
		t.Fatal(err)
	}

	threads := map[string]bool{
		receiveWithin(t, started): true,
		receiveWithin(t, started): true,
	}
	if !threads["topic-1"] || !threads["topic-2"] {
		t.Fatalf("并发启动的线程 = %+v", threads)
	}
	close(release)
	receiveWithin(t, done)
	receiveWithin(t, done)
}

func TestQueueLimitsGlobalConcurrency(t *testing.T) {
	const taskCount = 6
	started := make(chan struct{}, taskCount)
	release := make(chan struct{})
	done := make(chan struct{}, taskCount)
	var running atomic.Int32
	var maxRunning atomic.Int32

	queue, err := NewQueue(2, time.Second, 10, func(_ context.Context, _ bus.InboundMessage) {
		current := running.Add(1)
		for {
			maximum := maxRunning.Load()
			if current <= maximum || maxRunning.CompareAndSwap(maximum, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		running.Add(-1)
		done <- struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Stop()

	for index := range taskCount {
		address := bus.ConversationAddress{Channel: "telegram", AccountID: "bot", ChatID: fmt.Sprintf("chat-%d", index)}
		if err := queue.Submit(testMessage(address, fmt.Sprintf("message-%d", index))); err != nil {
			t.Fatal(err)
		}
	}
	receiveWithin(t, started)
	receiveWithin(t, started)
	select {
	case <-started:
		t.Fatal("启动的任务数超过全局并发上限")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for range taskCount {
		receiveWithin(t, done)
	}
	if got := maxRunning.Load(); got != 2 {
		t.Fatalf("全局最大并发数 = %d，期望 2", got)
	}
}

func TestQueueTimeoutAllowsNextMessage(t *testing.T) {
	firstStarted := make(chan struct{})
	firstErr := make(chan error, 1)
	secondDone := make(chan struct{})
	queue, err := NewQueue(1, 50*time.Millisecond, 10, func(ctx context.Context, msg bus.InboundMessage) {
		if msg.Text == "first" {
			close(firstStarted)
			<-ctx.Done()
			firstErr <- ctx.Err()
			return
		}
		close(secondDone)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Stop()

	address := bus.ConversationAddress{Channel: "feishu", AccountID: "bot", ChatID: "chat"}
	if err := queue.Submit(testMessage(address, "first")); err != nil {
		t.Fatal(err)
	}
	receiveWithin(t, firstStarted)
	if err := queue.Submit(testMessage(address, "second")); err != nil {
		t.Fatal(err)
	}
	if err := receiveWithin(t, firstErr); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("首条任务错误 = %v", err)
	}
	receiveWithin(t, secondDone)
}

func TestQueueRejectsFullConversationWithoutBlockingOtherAddresses(t *testing.T) {
	firstStarted := make(chan struct{})
	release := make(chan struct{})
	queue, err := NewQueue(1, time.Second, 1, func(_ context.Context, msg bus.InboundMessage) {
		if msg.Text == "first" {
			close(firstStarted)
		}
		<-release
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Stop()

	address := bus.ConversationAddress{Channel: "feishu", AccountID: "bot", ChatID: "chat"}
	if err := queue.Submit(testMessage(address, "first")); err != nil {
		t.Fatal(err)
	}
	receiveWithin(t, firstStarted)
	if err := queue.Submit(testMessage(address, "second")); err != nil {
		t.Fatal(err)
	}
	if err := queue.Submit(testMessage(address, "third")); !errors.Is(err, ErrConversationQueueFull) {
		t.Fatalf("满载提交错误 = %v", err)
	}

	other := bus.ConversationAddress{Channel: "feishu", AccountID: "bot", ChatID: "other"}
	if err := queue.Submit(testMessage(other, "other")); err != nil {
		t.Fatalf("其他会话提交失败: %v", err)
	}
	close(release)
}

func TestQueueStopCancelsRunningAndDropsPendingMessages(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	var handled atomic.Int32
	queue, err := NewQueue(1, time.Second, 10, func(ctx context.Context, _ bus.InboundMessage) {
		handled.Add(1)
		close(started)
		<-ctx.Done()
		close(cancelled)
	})
	if err != nil {
		t.Fatal(err)
	}

	address := bus.ConversationAddress{Channel: "terminal", AccountID: "local", ChatID: "default"}
	if err := queue.Submit(testMessage(address, "first")); err != nil {
		t.Fatal(err)
	}
	receiveWithin(t, started)
	if err := queue.Submit(testMessage(address, "second")); err != nil {
		t.Fatal(err)
	}
	queue.Stop()
	receiveWithin(t, cancelled)

	if got := handled.Load(); got != 1 {
		t.Fatalf("停止前实际处理数量 = %d，期望 1", got)
	}
	if err := queue.Submit(testMessage(address, "after-stop")); !errors.Is(err, ErrQueueStopped) {
		t.Fatalf("停止后提交错误 = %v", err)
	}
	queue.Stop()
}

func TestQueueRemovesEmptyConversationState(t *testing.T) {
	done := make(chan struct{})
	queue, err := NewQueue(1, time.Second, 1, func(context.Context, bus.InboundMessage) {
		close(done)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Stop()

	address := bus.ConversationAddress{Channel: "terminal", AccountID: "local", ChatID: "default"}
	if err := queue.Submit(testMessage(address, "message")); err != nil {
		t.Fatal(err)
	}
	receiveWithin(t, done)

	deadline := time.Now().Add(time.Second)
	for {
		queue.mu.Lock()
		count := len(queue.conversations)
		queue.mu.Unlock()
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("空会话状态没有被删除: %d", count)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestQueueConcurrentSubmit(t *testing.T) {
	const taskCount = 100
	done := make(chan struct{}, taskCount)
	queue, err := NewQueue(10, time.Second, taskCount, func(context.Context, bus.InboundMessage) {
		done <- struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Stop()

	address := bus.ConversationAddress{Channel: "telegram", AccountID: "bot", ChatID: "chat"}
	var waitGroup sync.WaitGroup
	var submitErrors atomic.Int32
	waitGroup.Add(taskCount)
	for index := range taskCount {
		go func() {
			defer waitGroup.Done()
			if err := queue.Submit(testMessage(address, fmt.Sprintf("message-%d", index))); err != nil {
				submitErrors.Add(1)
			}
		}()
	}
	waitGroup.Wait()
	if got := submitErrors.Load(); got != 0 {
		t.Fatalf("并发提交错误数量 = %d", got)
	}
	for range taskCount {
		receiveWithin(t, done)
	}
}
