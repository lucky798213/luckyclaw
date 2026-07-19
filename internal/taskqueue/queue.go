// Package taskqueue 提供按会话串行、跨会话并发的进程内任务队列。
package taskqueue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lucky798213/luckyclaw/internal/bus"
)

var (
	// ErrQueueStopped 表示任务队列已经停止，不再接受新消息。
	ErrQueueStopped = errors.New("task queue is stopped")
	// ErrConversationQueueFull 表示单个会话的等待队列已满。
	ErrConversationQueueFull = errors.New("conversation task queue is full")
)

// TaskHandler 处理一条已经取得全局并发槽的入站消息。
type TaskHandler func(context.Context, bus.InboundMessage)

// conversationState 保存一个会话尚未开始执行的消息。
type conversationState struct {
	pending []bus.InboundMessage
}

// Queue 管理按会话隔离的 FIFO 队列和全局并发限制。
type Queue struct {
	mu                        sync.Mutex
	conversations             map[bus.ConversationAddress]*conversationState
	sem                       chan struct{}
	taskTimeout               time.Duration
	maxPendingPerConversation int
	handler                   TaskHandler
	ctx                       context.Context
	cancel                    context.CancelFunc
	stopped                   bool
	stopOnce                  sync.Once
	workers                   sync.WaitGroup
}

// NewQueue 创建任务队列，所有并发和容量参数都必须大于零。
func NewQueue(
	maxConcurrent int,
	taskTimeout time.Duration,
	maxPendingPerConversation int,
	handler TaskHandler,
) (*Queue, error) {
	if maxConcurrent <= 0 {
		return nil, fmt.Errorf("max concurrent must be greater than zero")
	}
	if taskTimeout <= 0 {
		return nil, fmt.Errorf("task timeout must be greater than zero")
	}
	if maxPendingPerConversation <= 0 {
		return nil, fmt.Errorf("max pending per conversation must be greater than zero")
	}
	if handler == nil {
		return nil, fmt.Errorf("task handler cannot be nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Queue{
		conversations:             make(map[bus.ConversationAddress]*conversationState),
		sem:                       make(chan struct{}, maxConcurrent),
		taskTimeout:               taskTimeout,
		maxPendingPerConversation: maxPendingPerConversation,
		handler:                   handler,
		ctx:                       ctx,
		cancel:                    cancel,
	}, nil
}

// Submit 将消息追加到完整会话地址对应的 FIFO 队列。
func (q *Queue) Submit(msg bus.InboundMessage) error {
	address := msg.Address()

	// 阶段一：在同一把锁内完成停止检查、会话队列创建和容量判断。
	q.mu.Lock()
	if q.stopped {
		q.mu.Unlock()
		return ErrQueueStopped
	}

	state, exists := q.conversations[address]
	if !exists {
		state = &conversationState{}
		q.conversations[address] = state
	}
	if len(state.pending) >= q.maxPendingPerConversation {
		q.mu.Unlock()
		return ErrConversationQueueFull
	}

	// 阶段二：消息只追加到尾部；每个会话始终只有一个 worker 从头部消费。
	state.pending = append(state.pending, msg)
	if !exists {
		q.workers.Add(1)
	}
	q.mu.Unlock()

	// 阶段三：首条消息负责启动 worker，后续提交只入队，避免同会话并行执行。
	if !exists {
		go q.processConversation(address, state)
	}
	return nil
}

// processConversation 按提交顺序逐条执行同一会话的消息。
func (q *Queue) processConversation(address bus.ConversationAddress, state *conversationState) {
	defer q.workers.Done()
	defer q.removeConversation(address, state)

	for {
		// 阶段一：先申请全局信号量，限制不同会话同时占用的模型处理槽数量。
		select {
		case q.sem <- struct{}{}:
		case <-q.ctx.Done():
			return
		}

		// 阶段二：按 FIFO 取出当前会话下一条消息；队列清空后 worker 自动退出。
		msg, exists := q.nextMessage(address, state)
		if !exists {
			<-q.sem
			return
		}

		// 阶段三：每条消息拥有独立超时，超时后释放并发槽，让后续消息继续处理。
		taskCtx, cancel := context.WithTimeout(q.ctx, q.taskTimeout)
		q.handler(taskCtx, msg)
		cancel()
		<-q.sem
	}
}

// nextMessage 取出下一条消息；队列停止或会话清空时让 worker 退出。
func (q *Queue) nextMessage(address bus.ConversationAddress, state *conversationState) (bus.InboundMessage, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.stopped || len(state.pending) == 0 {
		if current := q.conversations[address]; current == state {
			delete(q.conversations, address)
		}
		return bus.InboundMessage{}, false
	}

	msg := state.pending[0]
	state.pending[0] = bus.InboundMessage{}
	state.pending = state.pending[1:]
	return msg, true
}

// removeConversation 清理 worker 提前退出时遗留的会话状态。
func (q *Queue) removeConversation(address bus.ConversationAddress, state *conversationState) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if current := q.conversations[address]; current == state {
		delete(q.conversations, address)
	}
}

// Stop 停止接收新任务、取消运行任务、丢弃等待任务并等待 worker 退出。
func (q *Queue) Stop() {
	q.stopOnce.Do(func() {
		q.mu.Lock()
		q.stopped = true
		for _, state := range q.conversations {
			state.pending = nil
		}
		q.mu.Unlock()
		q.cancel()
	})
	q.workers.Wait()
}
