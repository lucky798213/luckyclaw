// Package webui 提供嵌入 LuckyClaw 的本地网页工作台。
package webui

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lucky798213/luckyclaw/internal/agent"
	"github.com/lucky798213/luckyclaw/internal/bus"
	"github.com/lucky798213/luckyclaw/internal/config"
	"github.com/lucky798213/luckyclaw/internal/provider"
	"github.com/lucky798213/luckyclaw/internal/session"
)

const (
	webChannelName = "web"
	webAccountID   = "local-web"
	maxSoulBytes   = 64 << 10
	maxMessageBody = 64 << 10
	sseHeartbeat   = 15 * time.Second
)

//go:embed static/*
var staticFiles embed.FS

// Server 提供网页资源和本地控制面接口。
type Server struct {
	listen   string
	agents   *agent.Manager
	store    *session.SQLiteStore
	bindings []config.BindingConfig
	handler  http.Handler
	locks    sync.Map
}

type agentView struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	DefaultModel  string         `json:"default_model"`
	Models        []string       `json:"models"`
	Soul          string         `json:"soul,omitempty"`
	SoulPreview   string         `json:"soul_preview"`
	SessionCount  int            `json:"session_count"`
	ConnectedVia  []platformView `json:"connected_via"`
	LastActiveAt  string         `json:"last_active_at,omitempty"`
	RuntimeStatus string         `json:"runtime_status"`
}

type platformView struct {
	Channel   string `json:"channel"`
	AccountID string `json:"account_id"`
	Label     string `json:"label"`
}

type sessionView struct {
	Key          string            `json:"key"`
	Title        string            `json:"title"`
	Preview      string            `json:"preview"`
	ModelRef     string            `json:"model_ref"`
	MessageCount int               `json:"message_count"`
	CreatedAt    string            `json:"created_at"`
	UpdatedAt    string            `json:"updated_at"`
	Messages     []conversationMsg `json:"messages,omitempty"`
}

type conversationMsg struct {
	Role     string `json:"role"`
	Content  string `json:"content"`
	ToolName string `json:"tool_name,omitempty"`
}

type soulPayload struct {
	Soul string `json:"soul"`
}

type messagePayload struct {
	Text string `json:"text"`
}

type modelPayload struct {
	ModelRef string `json:"model_ref"`
}

// New 创建网页工作台服务器。
func New(listen string, agents *agent.Manager, store *session.SQLiteStore, bindings []config.BindingConfig) (*Server, error) {
	if strings.TrimSpace(listen) == "" {
		return nil, fmt.Errorf("web listen address cannot be empty")
	}
	if agents == nil {
		return nil, fmt.Errorf("agent manager cannot be nil")
	}
	if store == nil {
		return nil, fmt.Errorf("session store cannot be nil")
	}
	server := &Server{
		listen:   listen,
		agents:   agents,
		store:    store,
		bindings: append([]config.BindingConfig(nil), bindings...),
	}
	handler, err := server.routes()
	if err != nil {
		return nil, err
	}
	server.handler = handler
	return server, nil
}

// Handler 返回可供测试或外部服务器复用的 HTTP 处理器。
func (s *Server) Handler() http.Handler {
	return s.handler
}

// Start 启动服务器，并在上下文结束时优雅关闭。
func (s *Server) Start(ctx context.Context) (string, error) {
	listener, err := net.Listen("tcp", s.listen)
	if err != nil {
		return "", fmt.Errorf("listen web workspace on %s: %w", s.listen, err)
	}
	httpServer := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("关闭网页工作台失败: %v", err)
		}
	}()
	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("网页工作台已停止: %v", err)
		}
	}()
	return "http://" + listener.Addr().String(), nil
}

func (s *Server) routes() (http.Handler, error) {
	assets, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, fmt.Errorf("open embedded web assets: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(assets))))
	mux.HandleFunc("GET /api/agents", s.listAgents)
	mux.HandleFunc("GET /api/agents/{agentID}", s.getAgent)
	mux.HandleFunc("PUT /api/agents/{agentID}/soul", s.updateSoul)
	mux.HandleFunc("GET /api/agents/{agentID}/sessions", s.listSessions)
	mux.HandleFunc("POST /api/agents/{agentID}/sessions", s.createSession)
	mux.HandleFunc("GET /api/agents/{agentID}/sessions/{sessionKey}", s.getSession)
	mux.HandleFunc("POST /api/agents/{agentID}/sessions/{sessionKey}/messages", s.sendMessage)
	mux.HandleFunc("POST /api/agents/{agentID}/sessions/{sessionKey}/messages/stream", s.streamMessage)
	mux.HandleFunc("PUT /api/agents/{agentID}/sessions/{sessionKey}/model", s.updateModel)
	mux.HandleFunc("GET /", s.serveApp)
	return securityHeaders(mux), nil
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	views := make([]agentView, 0, len(s.agents.All()))
	for _, current := range s.agents.All() {
		view, err := s.buildAgentView(r.Context(), current, false)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "读取 Agent 信息失败")
			return
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": views})
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	current := s.agentFromRequest(w, r)
	if current == nil {
		return
	}
	view, err := s.buildAgentView(r.Context(), current, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取 Agent 信息失败")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) updateSoul(w http.ResponseWriter, r *http.Request) {
	current := s.agentFromRequest(w, r)
	if current == nil {
		return
	}
	var payload soulPayload
	if err := decodeJSON(w, r, &payload, maxSoulBytes); err != nil {
		return
	}
	payload.Soul = strings.TrimSpace(payload.Soul)
	if payload.Soul == "" {
		writeError(w, http.StatusBadRequest, "Soul 不能为空")
		return
	}
	if utf8.RuneCountInString(payload.Soul) > 20000 {
		writeError(w, http.StatusBadRequest, "Soul 不能超过 20000 个字符")
		return
	}
	if err := current.UpdateSoul(payload.Soul + "\n"); err != nil {
		writeError(w, http.StatusInternalServerError, "保存 Soul 失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"soul": payload.Soul, "saved": true})
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	current := s.agentFromRequest(w, r)
	if current == nil {
		return
	}
	summaries, err := s.store.ListByAgent(r.Context(), current.ID(), webChannelName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取会话列表失败")
		return
	}
	views := make([]sessionView, 0, len(summaries))
	for _, summary := range summaries {
		views = append(views, buildSessionView(summary, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": views})
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	current := s.agentFromRequest(w, r)
	if current == nil {
		return
	}
	chatID := "chat-" + randomID()
	created, err := current.NewSession(r.Context(), bus.ConversationAddress{
		Channel:   webChannelName,
		AccountID: webAccountID,
		ChatID:    chatID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "创建会话失败")
		return
	}
	view, err := s.sessionViewByKey(r.Context(), current.ID(), created.Key(), true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取新会话失败")
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	current := s.agentFromRequest(w, r)
	if current == nil {
		return
	}
	view, err := s.sessionViewByKey(r.Context(), current.ID(), r.PathValue("sessionKey"), true)
	if errors.Is(err, sessionNotFoundError{}) {
		writeError(w, http.StatusNotFound, "会话不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取会话失败")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	current := s.agentFromRequest(w, r)
	if current == nil {
		return
	}
	record, err := s.webSessionRecord(r.Context(), current.ID(), r.PathValue("sessionKey"))
	if errors.Is(err, sessionNotFoundError{}) {
		writeError(w, http.StatusNotFound, "会话不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取会话失败")
		return
	}
	var payload messagePayload
	if err := decodeJSON(w, r, &payload, maxMessageBody); err != nil {
		return
	}
	payload.Text = strings.TrimSpace(payload.Text)
	if payload.Text == "" {
		writeError(w, http.StatusBadRequest, "消息不能为空")
		return
	}
	if utf8.RuneCountInString(payload.Text) > 20000 {
		writeError(w, http.StatusBadRequest, "消息不能超过 20000 个字符")
		return
	}
	if payload.Text == "/new" {
		writeError(w, http.StatusBadRequest, "网页端请使用“新会话”按钮创建会话")
		return
	}
	lock := s.sessionLock(current.ID(), record.Key)
	if !lock.Lock(r.Context()) {
		return
	}
	defer lock.Unlock()
	reply := current.HandleMessage(r.Context(), webInboundMessage(current.ID(), record, payload.Text))
	view, err := s.sessionViewByKey(r.Context(), current.ID(), record.Key, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "刷新会话失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reply": reply, "session": view})
}

func (s *Server) streamMessage(w http.ResponseWriter, r *http.Request) {
	current := s.agentFromRequest(w, r)
	if current == nil {
		return
	}
	record, err := s.webSessionRecord(r.Context(), current.ID(), r.PathValue("sessionKey"))
	if errors.Is(err, sessionNotFoundError{}) {
		writeError(w, http.StatusNotFound, "会话不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取会话失败")
		return
	}
	var payload messagePayload
	if err := decodeJSON(w, r, &payload, maxMessageBody); err != nil {
		return
	}
	payload.Text = strings.TrimSpace(payload.Text)
	if payload.Text == "" {
		writeError(w, http.StatusBadRequest, "消息不能为空")
		return
	}
	if utf8.RuneCountInString(payload.Text) > 20000 {
		writeError(w, http.StatusBadRequest, "消息不能超过 20000 个字符")
		return
	}
	if payload.Text == "/new" {
		writeError(w, http.StatusBadRequest, "网页端请使用“新会话”按钮创建会话")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "当前服务器不支持流式响应")
		return
	}
	lock := s.sessionLock(current.ID(), record.Key)
	if !lock.Lock(r.Context()) {
		return
	}
	defer lock.Unlock()

	ctx, cancel := context.WithCancel(r.Context())
	events := current.HandleMessageStream(ctx, webInboundMessage(current.ID(), record, payload.Text))
	defer func() {
		cancel()
		// Agent 退出后再释放会话锁，防止下一条消息读到尚未结束的本轮状态。
		for range events {
		}
	}()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				cancel()
				return
			}
			flusher.Flush()
		case event, open := <-events:
			if !open {
				return
			}
			if err := writeSSEEvent(w, flusher, event); err != nil {
				cancel()
				return
			}
			if event.Type == agent.EventFinal || event.Type == agent.EventError {
				return
			}
		}
	}
}

func webInboundMessage(agentID string, record session.Record, text string) bus.InboundMessage {
	return bus.InboundMessage{
		Channel:   record.Address.Channel,
		AccountID: record.Address.AccountID,
		ChatID:    record.Address.ChatID,
		ThreadID:  record.Address.ThreadID,
		UserID:    "local-user",
		MessageID: "web-" + randomID(),
		Text:      text,
		AgentID:   agentID,
	}
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, event agent.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func (s *Server) updateModel(w http.ResponseWriter, r *http.Request) {
	current := s.agentFromRequest(w, r)
	if current == nil {
		return
	}
	record, err := s.webSessionRecord(r.Context(), current.ID(), r.PathValue("sessionKey"))
	if errors.Is(err, sessionNotFoundError{}) {
		writeError(w, http.StatusNotFound, "会话不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取会话失败")
		return
	}
	var payload modelPayload
	if err := decodeJSON(w, r, &payload, maxMessageBody); err != nil {
		return
	}
	payload.ModelRef = strings.TrimSpace(payload.ModelRef)
	command := "/model default"
	if payload.ModelRef != "" && payload.ModelRef != current.Model() {
		if !contains(current.Models(), payload.ModelRef) {
			writeError(w, http.StatusBadRequest, "该 Agent 不允许使用这个模型")
			return
		}
		command = "/model " + payload.ModelRef
	}
	lock := s.sessionLock(current.ID(), record.Key)
	if !lock.Lock(r.Context()) {
		return
	}
	defer lock.Unlock()
	current.HandleMessage(r.Context(), bus.InboundMessage{
		Channel:   record.Address.Channel,
		AccountID: record.Address.AccountID,
		ChatID:    record.Address.ChatID,
		ThreadID:  record.Address.ThreadID,
		Text:      command,
	})
	view, err := s.sessionViewByKey(r.Context(), current.ID(), record.Key, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "刷新会话失败")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) buildAgentView(ctx context.Context, current *agent.Agent, includeSoul bool) (agentView, error) {
	soul, err := current.ReadSoul()
	if err != nil {
		return agentView{}, err
	}
	summaries, err := s.store.ListByAgent(ctx, current.ID(), webChannelName)
	if err != nil {
		return agentView{}, err
	}
	view := agentView{
		ID:            current.ID(),
		Name:          current.Name(),
		DefaultModel:  current.Model(),
		Models:        current.Models(),
		SoulPreview:   compactText(soul, 88),
		SessionCount:  len(summaries),
		ConnectedVia:  s.platformsFor(current.ID()),
		RuntimeStatus: "ready",
	}
	if includeSoul {
		view.Soul = strings.TrimSpace(soul)
	}
	if len(summaries) > 0 {
		view.LastActiveAt = summaries[0].UpdatedAt.Format(time.RFC3339)
	}
	return view, nil
}

func (s *Server) platformsFor(agentID string) []platformView {
	platforms := []platformView{{Channel: webChannelName, AccountID: webAccountID, Label: "网页工作台"}}
	seen := map[string]struct{}{webChannelName + "\x00" + webAccountID: {}}
	for _, binding := range s.bindings {
		if binding.AgentID != agentID {
			continue
		}
		key := binding.Channel + "\x00" + binding.AccountID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		platforms = append(platforms, platformView{
			Channel:   binding.Channel,
			AccountID: binding.AccountID,
			Label:     platformLabel(binding.Channel),
		})
	}
	return platforms
}

func (s *Server) agentFromRequest(w http.ResponseWriter, r *http.Request) *agent.Agent {
	current := s.agents.AgentByID(r.PathValue("agentID"))
	if current == nil {
		writeError(w, http.StatusNotFound, "Agent 不存在")
	}
	return current
}

func (s *Server) sessionViewByKey(ctx context.Context, agentID, key string, includeMessages bool) (sessionView, error) {
	if _, err := s.webSessionRecord(ctx, agentID, key); err != nil {
		return sessionView{}, err
	}
	summaries, err := s.store.ListByAgent(ctx, agentID, webChannelName)
	if err != nil {
		return sessionView{}, err
	}
	for _, summary := range summaries {
		if summary.Key == key {
			return buildSessionView(summary, includeMessages), nil
		}
	}
	return sessionView{}, sessionNotFoundError{}
}

func (s *Server) webSessionRecord(ctx context.Context, agentID, key string) (session.Record, error) {
	record, exists, err := s.store.LoadByKey(ctx, agentID, key)
	if err != nil {
		return session.Record{}, err
	}
	if !exists || record.Address.Channel != webChannelName {
		return session.Record{}, sessionNotFoundError{}
	}
	return record, nil
}

type sessionNotFoundError struct{}

func (sessionNotFoundError) Error() string { return "session not found" }

func buildSessionView(summary session.Summary, includeMessages bool) sessionView {
	view := sessionView{
		Key:          summary.Key,
		Title:        sessionTitle(summary.Messages),
		Preview:      sessionPreview(summary.Messages),
		ModelRef:     summary.ModelRef,
		MessageCount: visibleMessageCount(summary.Messages),
		CreatedAt:    summary.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    summary.UpdatedAt.Format(time.RFC3339),
	}
	if includeMessages {
		view.Messages = visibleMessages(summary.Messages)
	}
	return view
}

func visibleMessages(messages []provider.Message) []conversationMsg {
	visible := make([]conversationMsg, 0, len(messages))
	for _, message := range messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		switch message.Role {
		case "user", "assistant":
			visible = append(visible, conversationMsg{Role: message.Role, Content: content})
		case "tool":
			visible = append(visible, conversationMsg{Role: "tool", Content: content, ToolName: message.Name})
		}
	}
	return visible
}

func visibleMessageCount(messages []provider.Message) int {
	return len(visibleMessages(messages))
}

func sessionTitle(messages []provider.Message) string {
	for _, message := range messages {
		if message.Role == "user" && strings.TrimSpace(message.Content) != "" {
			return compactText(message.Content, 24)
		}
	}
	return "新会话"
}

func sessionPreview(messages []provider.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if (message.Role == "assistant" || message.Role == "user") && strings.TrimSpace(message.Content) != "" {
			return compactText(message.Content, 42)
		}
	}
	return "从一句话开始，让它为你工作"
}

func compactText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func platformLabel(channel string) string {
	switch strings.ToLower(channel) {
	case "terminal":
		return "本地终端"
	case "feishu":
		return "飞书"
	case "telegram":
		return "Telegram"
	default:
		return channel
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type contextLock struct {
	token chan struct{}
}

func newContextLock() *contextLock {
	lock := &contextLock{token: make(chan struct{}, 1)}
	lock.token <- struct{}{}
	return lock
}

func (l *contextLock) Lock(ctx context.Context) bool {
	select {
	case <-l.token:
		return true
	case <-ctx.Done():
		return false
	}
}

func (l *contextLock) Unlock() {
	l.token <- struct{}{}
}

func (s *Server) sessionLock(agentID, sessionKey string) *contextLock {
	key := agentID + "\x00" + sessionKey
	lock, _ := s.locks.LoadOrStore(key, newContextLock())
	return lock.(*contextLock)
}

func randomID() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}

func (s *Server) serveApp(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/static/") {
		http.NotFound(w, r)
		return
	}
	content, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "加载网页失败")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "请求内容格式不正确")
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("写入网页响应失败: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
