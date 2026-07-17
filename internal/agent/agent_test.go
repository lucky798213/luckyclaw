package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lucky798213/luckyclaw/internal/bus"
	"github.com/lucky798213/luckyclaw/internal/provider"
	"github.com/lucky798213/luckyclaw/internal/session"
	toolruntime "github.com/lucky798213/luckyclaw/internal/tools"
)

type fakeProvider struct {
	mu       sync.Mutex
	requests [][]provider.Message
	models   []string
	reply    string
	err      error
}

type singleMessageStream struct {
	message *provider.Message
	done    bool
}

func (s *singleMessageStream) Next() (provider.StreamChunk, error) {
	if s.done {
		return provider.StreamChunk{}, io.EOF
	}
	s.done = true
	return provider.StreamChunk{Message: s.message, Done: true}, nil
}

func (s *singleMessageStream) Close() error { return nil }

func (p *fakeProvider) Chat(_ context.Context, messages []provider.Message, _ []provider.Tool, model string, _ int, _ float64) (*provider.Message, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	request := append([]provider.Message(nil), messages...)
	p.requests = append(p.requests, request)
	p.models = append(p.models, model)
	if p.err != nil {
		return nil, p.err
	}
	reply := p.reply
	if reply == "" {
		reply = "reply"
	}
	return &provider.Message{Role: "assistant", Content: reply}, nil
}

func (p *fakeProvider) ChatStream(ctx context.Context, messages []provider.Message, tools []provider.Tool, model string, maxTokens int, temperature float64) (provider.Stream, error) {
	message, err := p.Chat(ctx, messages, tools, model, maxTokens, temperature)
	if err != nil {
		return nil, err
	}
	return &singleMessageStream{message: message}, nil
}

func (p *fakeProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func (p *fakeProvider) calledModels() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.models...)
}

func writeSoul(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newProviderManager(t *testing.T, name string, models []string, prov provider.Provider) *provider.Manager {
	t.Helper()
	manager := provider.NewManager()
	if err := manager.Register(name, prov, models); err != nil {
		t.Fatal(err)
	}
	return manager
}

func newTestAgent(t *testing.T, providers *provider.Manager, soulPath, defaultModel string, models []string) *Agent {
	t.Helper()
	a, err := New(Options{
		ID:           "lucky",
		Name:         "LuckyClaw",
		DefaultModel: defaultModel,
		Models:       models,
		SoulPath:     soulPath,
		Tools:        newDefaultToolRegistry(t),
	}, providers)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func newDefaultToolRegistry(t *testing.T) toolruntime.Registry {
	t.Helper()
	registry, err := toolruntime.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func inbound(chatID, text string) bus.InboundMessage {
	return bus.InboundMessage{
		Channel:   "terminal",
		AccountID: "local",
		ChatID:    chatID,
		Text:      text,
	}
}

func TestNewValidatesToolLoopOptions(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	providers := newProviderManager(t, "test", []string{"model"}, &fakeProvider{})
	base := Options{
		ID:           "lucky",
		Name:         "LuckyClaw",
		DefaultModel: "test/model",
		Models:       []string{"test/model"},
		SoulPath:     soulPath,
		Tools:        newDefaultToolRegistry(t),
	}
	tests := []struct {
		name  string
		apply func(*Options)
		want  string
	}{
		{name: "缺少工具注册表", apply: func(options *Options) { options.Tools = nil }, want: "tool registry"},
		{name: "迭代数为负", apply: func(options *Options) { options.MaxToolIterations = -1 }, want: "max tool iterations"},
		{name: "超时为负", apply: func(options *Options) { options.ToolTimeout = -time.Second }, want: "tool timeout"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := base
			test.apply(&options)
			_, err := New(options, providers)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestHandleMessageIncludesPreviousMessages(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	prov := &fakeProvider{}
	a := newTestAgent(t, newProviderManager(t, "test", []string{"model"}, prov), soulPath, "test/model", []string{"test/model"})

	if got := a.HandleMessage(context.Background(), inbound("chat-1", "first")); got != "reply" {
		t.Fatalf("first reply = %q", got)
	}
	if got := a.HandleMessage(context.Background(), inbound("chat-1", "second")); got != "reply" {
		t.Fatalf("second reply = %q", got)
	}

	want := []provider.Message{
		{Role: "system", Content: "friendly"},
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "reply"},
		{Role: "user", Content: "second"},
	}
	assertMessages(t, prov.requests[1], want)
}

func TestHandleMessageStreamEmitsFinalEvent(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	prov := &fakeProvider{reply: "stream reply"}
	a := newTestAgent(t, newProviderManager(t, "test", []string{"model"}, prov), soulPath, "test/model", []string{"test/model"})

	events := a.HandleMessageStream(context.Background(), inbound("chat-stream", "hello"))
	event, ok := <-events
	if !ok {
		t.Fatal("event stream closed before final event")
	}
	if event.Type != EventFinal || event.Data.Content != "stream reply" {
		t.Fatalf("event = %+v", event)
	}
	if _, ok := <-events; ok {
		t.Fatal("event stream contains unexpected extra event")
	}
}

func TestHandleMessageReloadsSoul(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	prov := &fakeProvider{}
	a := newTestAgent(t, newProviderManager(t, "test", []string{"model"}, prov), soulPath, "test/model", []string{"test/model"})

	a.HandleMessage(context.Background(), inbound("chat-1", "first"))
	writeSoul(t, soulPath, "serious")
	a.HandleMessage(context.Background(), inbound("chat-1", "second"))

	if got := prov.requests[1][0]; got.Role != "system" || got.Content != "serious" {
		t.Fatalf("system message = %+v, want updated soul", got)
	}
}

func TestResetClearsConversationAndModelSelection(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	deepseek := &fakeProvider{}
	openai := &fakeProvider{}
	providers := newProviderManager(t, "deepseek", []string{"chat"}, deepseek)
	if err := providers.Register("openai", openai, []string{"mini"}); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, providers, soulPath, "deepseek/chat", []string{"deepseek/chat", "openai/mini"})

	a.HandleMessage(context.Background(), inbound("chat-1", "/model openai/mini"))
	a.HandleMessage(context.Background(), inbound("chat-1", "first"))
	if got := a.HandleMessage(context.Background(), inbound("chat-1", "/new")); got == "" {
		t.Fatal("reset reply is empty")
	}
	a.HandleMessage(context.Background(), inbound("chat-1", "after reset"))

	if openai.callCount() != 1 || deepseek.callCount() != 1 {
		t.Fatalf("provider calls: openai=%d deepseek=%d", openai.callCount(), deepseek.callCount())
	}
	want := []provider.Message{
		{Role: "system", Content: "friendly"},
		{Role: "user", Content: "after reset"},
	}
	assertMessages(t, deepseek.requests[0], want)
}

func TestHandleMessageFailureDoesNotSaveMessages(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	prov := &fakeProvider{err: errors.New("request failed")}
	a := newTestAgent(t, newProviderManager(t, "test", []string{"model"}, prov), soulPath, "test/model", []string{"test/model"})

	if got := a.HandleMessage(context.Background(), inbound("chat-1", "failed")); got != handleMessageErrorReply {
		t.Fatalf("failure reply = %q", got)
	}
	prov.err = nil
	a.HandleMessage(context.Background(), inbound("chat-1", "retry"))

	want := []provider.Message{
		{Role: "system", Content: "friendly"},
		{Role: "user", Content: "retry"},
	}
	assertMessages(t, prov.requests[1], want)
}

func TestHandleMessageIsolatesChats(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	prov := &fakeProvider{}
	a := newTestAgent(t, newProviderManager(t, "test", []string{"model"}, prov), soulPath, "test/model", []string{"test/model"})

	a.HandleMessage(context.Background(), inbound("chat-a", "from a"))
	a.HandleMessage(context.Background(), inbound("chat-b", "from b"))
	a.HandleMessage(context.Background(), inbound("chat-a", "a again"))

	wantChatB := []provider.Message{
		{Role: "system", Content: "friendly"},
		{Role: "user", Content: "from b"},
	}
	assertMessages(t, prov.requests[1], wantChatB)

	wantChatA := []provider.Message{
		{Role: "system", Content: "friendly"},
		{Role: "user", Content: "from a"},
		{Role: "assistant", Content: "reply"},
		{Role: "user", Content: "a again"},
	}
	assertMessages(t, prov.requests[2], wantChatA)
}

func TestHandleMessageIsolatesThreads(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	prov := &fakeProvider{}
	a := newTestAgent(t, newProviderManager(t, "test", []string{"model"}, prov), soulPath, "test/model", []string{"test/model"})

	first := inbound("chat-1", "from topic a")
	first.ThreadID = "topic-a"
	second := inbound("chat-1", "from topic b")
	second.ThreadID = "topic-b"
	firstAgain := inbound("chat-1", "topic a again")
	firstAgain.ThreadID = "topic-a"
	a.HandleMessage(context.Background(), first)
	a.HandleMessage(context.Background(), second)
	a.HandleMessage(context.Background(), firstAgain)

	wantTopicB := []provider.Message{
		{Role: "system", Content: "friendly"},
		{Role: "user", Content: "from topic b"},
	}
	assertMessages(t, prov.requests[1], wantTopicB)

	wantTopicA := []provider.Message{
		{Role: "system", Content: "friendly"},
		{Role: "user", Content: "from topic a"},
		{Role: "assistant", Content: "reply"},
		{Role: "user", Content: "topic a again"},
	}
	assertMessages(t, prov.requests[2], wantTopicA)
}

func TestMessageModelOverrideDoesNotPersist(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	deepseek := &fakeProvider{}
	openai := &fakeProvider{}
	providers := newProviderManager(t, "deepseek", []string{"chat"}, deepseek)
	if err := providers.Register("openai", openai, []string{"mini"}); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, providers, soulPath, "deepseek/chat", []string{"deepseek/chat", "openai/mini"})

	a.HandleMessage(context.Background(), inbound("chat-1", "default"))
	override := inbound("chat-1", "override")
	override.ModelRef = "openai/mini"
	a.HandleMessage(context.Background(), override)
	a.HandleMessage(context.Background(), inbound("chat-1", "default again"))

	if got := deepseek.calledModels(); len(got) != 2 || got[0] != "chat" || got[1] != "chat" {
		t.Fatalf("deepseek models = %#v", got)
	}
	if got := openai.calledModels(); len(got) != 1 || got[0] != "mini" {
		t.Fatalf("openai models = %#v", got)
	}
}

func TestMessageModelOverrideHasPriorityOverSessionModel(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	deepseek := &fakeProvider{}
	openai := &fakeProvider{}
	providers := newProviderManager(t, "deepseek", []string{"chat"}, deepseek)
	if err := providers.Register("openai", openai, []string{"mini"}); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, providers, soulPath, "deepseek/chat", []string{"deepseek/chat", "openai/mini"})

	a.HandleMessage(context.Background(), inbound("chat-1", "/model openai/mini"))
	override := inbound("chat-1", "one message")
	override.ModelRef = "deepseek/chat"
	a.HandleMessage(context.Background(), override)
	a.HandleMessage(context.Background(), inbound("chat-1", "session model again"))

	if deepseek.callCount() != 1 || openai.callCount() != 1 {
		t.Fatalf("provider calls: deepseek=%d openai=%d", deepseek.callCount(), openai.callCount())
	}
}

func TestModelCommandIsSessionScopedAndCanRestoreDefault(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	deepseek := &fakeProvider{}
	openai := &fakeProvider{}
	providers := newProviderManager(t, "deepseek", []string{"chat"}, deepseek)
	if err := providers.Register("openai", openai, []string{"mini"}); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, providers, soulPath, "deepseek/chat", []string{"deepseek/chat", "openai/mini"})

	if got := a.HandleMessage(context.Background(), inbound("chat-a", "/model openai/mini")); got != "当前会话模型已切换为: openai/mini" {
		t.Fatalf("switch reply = %q", got)
	}
	a.HandleMessage(context.Background(), inbound("chat-a", "from a"))
	a.HandleMessage(context.Background(), inbound("chat-b", "from b"))
	if got := a.HandleMessage(context.Background(), inbound("chat-a", "/model default")); got != "已恢复默认模型: deepseek/chat" {
		t.Fatalf("default reply = %q", got)
	}
	a.HandleMessage(context.Background(), inbound("chat-a", "a default"))

	if openai.callCount() != 1 || deepseek.callCount() != 2 {
		t.Fatalf("provider calls: openai=%d deepseek=%d", openai.callCount(), deepseek.callCount())
	}
}

func TestModelCommandListsCurrentDefaultAndAllowedModels(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	providers := newProviderManager(t, "deepseek", []string{"chat"}, &fakeProvider{})
	if err := providers.Register("openai", &fakeProvider{}, []string{"mini"}); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, providers, soulPath, "deepseek/chat", []string{"openai/mini", "deepseek/chat"})

	reply := a.HandleMessage(context.Background(), inbound("chat-1", "/model list"))
	for _, want := range []string{
		"当前模型: deepseek/chat",
		"默认模型: deepseek/chat",
		"- deepseek/chat\n- openai/mini",
	} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply = %q, missing %q", reply, want)
		}
	}
}

func TestInvalidModelDoesNotCallProvider(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	prov := &fakeProvider{}
	a := newTestAgent(t, newProviderManager(t, "deepseek", []string{"chat"}, prov), soulPath, "deepseek/chat", []string{"deepseek/chat"})

	msg := inbound("chat-1", "hello")
	msg.ModelRef = "openai/mini"
	if got := a.HandleMessage(context.Background(), msg); got == handleMessageErrorReply {
		t.Fatalf("invalid model returned generic error: %q", got)
	}
	if prov.callCount() != 0 {
		t.Fatalf("provider was called %d times", prov.callCount())
	}
	if got := a.HandleMessage(context.Background(), inbound("chat-1", "/model openai/mini")); got == "" {
		t.Fatal("model command error is empty")
	}
	if prov.callCount() != 0 {
		t.Fatalf("provider was called after command: %d", prov.callCount())
	}
}

func TestAgentCanCallDifferentProviderAPIBase(t *testing.T) {
	makeServer := func(wantModel, reply string, received chan<- string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			received <- body.Model
			if body.Model != wantModel {
				t.Errorf("model = %q, want %q", body.Model, wantModel)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": reply}}},
			})
		}))
	}

	deepseekModels := make(chan string, 1)
	openaiModels := make(chan string, 1)
	deepseekServer := makeServer("deepseek-chat", "deepseek", deepseekModels)
	defer deepseekServer.Close()
	openaiServer := makeServer("gpt-4.1-mini", "openai", openaiModels)
	defer openaiServer.Close()

	deepseekProvider, err := provider.NewOpenAI("key", deepseekServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	openaiProvider, err := provider.NewOpenAI("key", openaiServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	providers := newProviderManager(t, "deepseek", []string{"deepseek-chat"}, deepseekProvider)
	if err := providers.Register("openai", openaiProvider, []string{"gpt-4.1-mini"}); err != nil {
		t.Fatal(err)
	}
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	a := newTestAgent(t, providers, soulPath, "deepseek/deepseek-chat", []string{"deepseek/deepseek-chat", "openai/gpt-4.1-mini"})

	if got := a.HandleMessage(context.Background(), inbound("chat-1", "default")); got != "deepseek" {
		t.Fatalf("default reply = %q", got)
	}
	override := inbound("chat-1", "override")
	override.ModelRef = "openai/gpt-4.1-mini"
	if got := a.HandleMessage(context.Background(), override); got != "openai" {
		t.Fatalf("override reply = %q", got)
	}
	if got := <-deepseekModels; got != "deepseek-chat" {
		t.Fatalf("deepseek model = %q", got)
	}
	if got := <-openaiModels; got != "gpt-4.1-mini" {
		t.Fatalf("openai model = %q", got)
	}
}

func TestAgentRestoresHistoryAndSessionModelAfterRestart(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	soulPath := filepath.Join(tempDir, "SOUL.md")
	databasePath := filepath.Join(tempDir, "sessions.db")
	writeSoul(t, soulPath, "friendly")

	firstStore, err := session.OpenSQLite(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	firstDeepseek := &fakeProvider{}
	firstOpenAI := &fakeProvider{reply: "first reply"}
	firstProviders := newProviderManager(t, "deepseek", []string{"chat"}, firstDeepseek)
	if err := firstProviders.Register("openai", firstOpenAI, []string{"mini"}); err != nil {
		t.Fatal(err)
	}
	firstAgent, err := New(Options{
		ID:           "lucky",
		Name:         "LuckyClaw",
		DefaultModel: "deepseek/chat",
		Models:       []string{"deepseek/chat", "openai/mini"},
		SoulPath:     soulPath,
		SessionStore: firstStore,
		Tools:        newDefaultToolRegistry(t),
	}, firstProviders)
	if err != nil {
		t.Fatal(err)
	}
	firstAgent.HandleMessage(ctx, inbound("chat-1", "/model openai/mini"))
	if got := firstAgent.HandleMessage(ctx, inbound("chat-1", "first")); got != "first reply" {
		t.Fatalf("first reply = %q", got)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore, err := session.OpenSQLite(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	secondDeepseek := &fakeProvider{}
	secondOpenAI := &fakeProvider{reply: "second reply"}
	secondProviders := newProviderManager(t, "deepseek", []string{"chat"}, secondDeepseek)
	if err := secondProviders.Register("openai", secondOpenAI, []string{"mini"}); err != nil {
		t.Fatal(err)
	}
	secondAgent, err := New(Options{
		ID:           "lucky",
		Name:         "LuckyClaw",
		DefaultModel: "deepseek/chat",
		Models:       []string{"deepseek/chat", "openai/mini"},
		SoulPath:     soulPath,
		SessionStore: secondStore,
		Tools:        newDefaultToolRegistry(t),
	}, secondProviders)
	if err != nil {
		t.Fatal(err)
	}
	if got := secondAgent.HandleMessage(ctx, inbound("chat-1", "second")); got != "second reply" {
		t.Fatalf("second reply = %q", got)
	}
	if secondDeepseek.callCount() != 0 || secondOpenAI.callCount() != 1 {
		t.Fatalf("provider calls after restart: deepseek=%d openai=%d", secondDeepseek.callCount(), secondOpenAI.callCount())
	}
	want := []provider.Message{
		{Role: "system", Content: "friendly"},
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "first reply"},
		{Role: "user", Content: "second"},
	}
	assertMessages(t, secondOpenAI.requests[0], want)
}

func assertMessages(t *testing.T, got, want []provider.Message) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("message count = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content {
			t.Fatalf("message[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
