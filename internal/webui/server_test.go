package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lucky798213/luckyclaw/internal/agent"
	"github.com/lucky798213/luckyclaw/internal/provider"
	"github.com/lucky798213/luckyclaw/internal/session"
	"github.com/lucky798213/luckyclaw/internal/tools"
)

type webTestProvider struct{}

type webTestStream struct {
	step int
}

func (s *webTestStream) Next() (provider.StreamChunk, error) {
	switch s.step {
	case 0:
		s.step++
		return provider.StreamChunk{Delta: "网页"}, nil
	case 1:
		s.step++
		return provider.StreamChunk{Delta: "回复"}, nil
	case 2:
		s.step++
		return provider.StreamChunk{Done: true, Message: &provider.Message{Role: "assistant", Content: "网页回复"}}, nil
	default:
		return provider.StreamChunk{}, io.EOF
	}
}

func (s *webTestStream) Close() error { return nil }

func (webTestProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Message, error) {
	return &provider.Message{Role: "assistant", Content: "网页回复"}, nil
}

func (webTestProvider) ChatStream(_ context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (provider.Stream, error) {
	return &webTestStream{}, nil
}

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	temporary := t.TempDir()
	soulPath := filepath.Join(temporary, "SOUL.md")
	if err := os.WriteFile(soulPath, []byte("你是测试助手。\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	providerManager := provider.NewManager()
	if err := providerManager.Register("test", webTestProvider{}, []string{"chat"}); err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	store, err := session.OpenSQLite(filepath.Join(temporary, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	currentWithStore, err := agent.New(agent.Options{
		ID:           "lucky",
		Name:         "LuckyClaw",
		DefaultModel: "test/chat",
		Models:       []string{"test/chat"},
		SoulPath:     soulPath,
		SessionStore: store,
		Tools:        registry,
	}, providerManager)
	if err != nil {
		t.Fatal(err)
	}
	agentManager, err := agent.NewManager(map[string]*agent.Agent{"lucky": currentWithStore}, "lucky")
	if err != nil {
		t.Fatal(err)
	}
	server, err := New("127.0.0.1:0", agentManager, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	return server, soulPath
}

func performJSONRequest(t *testing.T, handler http.Handler, method, path string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, &body)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestServerListsAgentsCreatesSessionAndChats(t *testing.T) {
	server, _ := newTestServer(t)
	list := performJSONRequest(t, server.Handler(), http.MethodGet, "/api/agents", nil)
	if list.Code != http.StatusOK || !bytes.Contains(list.Body.Bytes(), []byte(`"id":"lucky"`)) {
		t.Fatalf("list agents status=%d body=%s", list.Code, list.Body.String())
	}

	created := performJSONRequest(t, server.Handler(), http.MethodPost, "/api/agents/lucky/sessions", nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create session status=%d body=%s", created.Code, created.Body.String())
	}
	var createdSession sessionView
	if err := json.NewDecoder(created.Body).Decode(&createdSession); err != nil {
		t.Fatal(err)
	}
	if createdSession.Key == "" || createdSession.Title != "新会话" {
		t.Fatalf("created session = %+v", createdSession)
	}

	chat := performJSONRequest(t, server.Handler(), http.MethodPost,
		"/api/agents/lucky/sessions/"+createdSession.Key+"/messages",
		messagePayload{Text: "你好"},
	)
	if chat.Code != http.StatusOK || !bytes.Contains(chat.Body.Bytes(), []byte("网页回复")) {
		t.Fatalf("chat status=%d body=%s", chat.Code, chat.Body.String())
	}

	sessions := performJSONRequest(t, server.Handler(), http.MethodGet, "/api/agents/lucky/sessions", nil)
	if sessions.Code != http.StatusOK || !bytes.Contains(sessions.Body.Bytes(), []byte(`"title":"你好"`)) {
		t.Fatalf("list sessions status=%d body=%s", sessions.Code, sessions.Body.String())
	}
}

func TestServerUpdatesSoul(t *testing.T) {
	server, soulPath := newTestServer(t)
	updated := performJSONRequest(t, server.Handler(), http.MethodPut, "/api/agents/lucky/soul", soulPayload{Soul: "你是严谨的研究助手。"})
	if updated.Code != http.StatusOK {
		t.Fatalf("update soul status=%d body=%s", updated.Code, updated.Body.String())
	}
	content, err := os.ReadFile(soulPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "你是严谨的研究助手。\n" {
		t.Fatalf("soul = %q", content)
	}
}

func TestServerStreamsChatEvents(t *testing.T) {
	server, _ := newTestServer(t)
	created := performJSONRequest(t, server.Handler(), http.MethodPost, "/api/agents/lucky/sessions", nil)
	var createdSession sessionView
	if err := json.NewDecoder(created.Body).Decode(&createdSession); err != nil {
		t.Fatal(err)
	}
	response := performJSONRequest(t, server.Handler(), http.MethodPost,
		"/api/agents/lucky/sessions/"+createdSession.Key+"/messages/stream",
		messagePayload{Text: "你好"},
	)
	if response.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	if response.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatal("缺少禁用代理缓冲响应头")
	}
	body := response.Body.String()
	if !strings.Contains(body, `"type":"token_delta"`) || !strings.Contains(body, `"delta":"网页"`) || !strings.Contains(body, `"type":"final"`) {
		t.Fatalf("stream body=%s", body)
	}
}

func TestContextLockStopsWaitingAfterCancellation(t *testing.T) {
	lock := newContextLock()
	if !lock.Lock(context.Background()) {
		t.Fatal("首次获取锁失败")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if lock.Lock(ctx) {
		t.Fatal("上下文取消后仍然获取到锁")
	}
	lock.Unlock()
}

func TestOpenAIChatCompletionsReturnsCompatibleResponseWithoutSession(t *testing.T) {
	server, _ := newTestServer(t)
	response := performJSONRequest(t, server.Handler(), http.MethodPost, "/v1/chat/completions", map[string]any{
		"model": "test/chat",
		"messages": []map[string]string{
			{"role": "developer", "content": "回答要简短"},
			{"role": "user", "content": "你好"},
		},
		"temperature": 0,
		"max_tokens":  128,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("completion status=%d body=%s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["object"] != "chat.completion" || body["model"] != "test/chat" {
		t.Fatalf("completion body=%#v", body)
	}
	choices := body["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	if message["role"] != "assistant" || message["content"] != "网页回复" {
		t.Fatalf("message=%#v", message)
	}
	sessions := performJSONRequest(t, server.Handler(), http.MethodGet, "/api/agents/lucky/sessions", nil)
	if !bytes.Contains(sessions.Body.Bytes(), []byte(`"sessions":[]`)) {
		t.Fatalf("无状态请求写入了会话: %s", sessions.Body.String())
	}
}

func TestOpenAIChatCompletionsStreamsRoleContentAndDone(t *testing.T) {
	server, _ := newTestServer(t)
	response := performJSONRequest(t, server.Handler(), http.MethodPost, "/v1/chat/completions", map[string]any{
		"model":    "test/chat",
		"messages": []map[string]string{{"role": "user", "content": "你好"}},
		"stream":   true,
	})
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" {
		t.Fatalf("stream status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	body := response.Body.String()
	rolePosition := strings.Index(body, `"delta":{"role":"assistant"}`)
	contentPosition := strings.Index(body, `"delta":{"content":"网页"}`)
	finishPosition := strings.Index(body, `"finish_reason":"stop"`)
	donePosition := strings.Index(body, "data: [DONE]")
	if rolePosition < 0 || contentPosition <= rolePosition || finishPosition <= contentPosition || donePosition <= finishPosition {
		t.Fatalf("OpenAI 流顺序不正确: %s", body)
	}
}

func TestOpenAIChatCompletionsRejectsUnsupportedClientTools(t *testing.T) {
	server, _ := newTestServer(t)
	response := performJSONRequest(t, server.Handler(), http.MethodPost, "/v1/chat/completions", map[string]any{
		"model":    "test/chat",
		"messages": []map[string]string{{"role": "user", "content": "你好"}},
		"tools":    []any{},
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !bytes.Contains(response.Body.Bytes(), []byte(`"type":"invalid_request_error"`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"param":"tools"`)) {
		t.Fatalf("error body=%s", response.Body.String())
	}
}
