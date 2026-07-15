package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"syscall"

	"github.com/lucky798213/luckyclaw/internal/agent"
	"github.com/lucky798213/luckyclaw/internal/bus"
	"github.com/lucky798213/luckyclaw/internal/channels"
	"github.com/lucky798213/luckyclaw/internal/config"
	"github.com/lucky798213/luckyclaw/internal/gateway"
	"github.com/lucky798213/luckyclaw/internal/provider"
	"github.com/lucky798213/luckyclaw/internal/session"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	providerManager := provider.NewManager()
	if err := providerManager.RegisterAll(providerDefinitions(cfg.Providers)); err != nil {
		log.Fatal(err)
	}

	sessionStore, err := session.OpenSQLite(cfg.Storage.Path)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := sessionStore.Close(); err != nil {
			log.Printf("关闭会话数据库失败: %v", err)
		}
	}()

	agents, err := buildAgents(cfg.Agents, providerManager, sessionStore)
	if err != nil {
		log.Fatal(err)
	}
	agentManager, err := agent.NewManager(agents, cfg.DefaultAgent)
	if err != nil {
		log.Fatal(err)
	}

	messageBus := bus.New()
	messageGateway, err := gateway.NewWithTaskQueueConfig(messageBus, agentManager, cfg.Bindings, cfg.TaskQueue)
	if err != nil {
		log.Fatal(err)
	}
	channelManager, err := channels.NewManager(messageBus)
	if err != nil {
		log.Fatal(err)
	}
	terminal, err := channels.NewTerminal(os.Stdin, os.Stdout, messageBus)
	if err != nil {
		log.Fatal(err)
	}
	if err := channelManager.Register(terminal); err != nil {
		log.Fatal(err)
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()

	gatewayDone := make(chan struct{})
	go func() {
		defer close(gatewayDone)
		messageGateway.Run(ctx)
	}()
	channelManager.Start(ctx)

	waitForShutdown(signalCtx, terminal.Done(), gatewayDone, cancel)
}

// waitForShutdown 统一处理退出信号，并等待 Gateway 完成任务取消和资源清理。
func waitForShutdown(
	signalCtx context.Context,
	terminalDone <-chan struct{},
	gatewayDone <-chan struct{},
	cancel context.CancelFunc,
) {
	select {
	case <-signalCtx.Done():
	case <-terminalDone:
	case <-gatewayDone:
		cancel()
		return
	}
	cancel()
	<-gatewayDone
}

func providerDefinitions(configs map[string]config.ProviderConfig) map[string]provider.Definition {
	definitions := make(map[string]provider.Definition, len(configs))
	for name, providerCfg := range configs {
		definitions[name] = provider.Definition{
			APIKey:   providerCfg.APIKey,
			APIBase:  providerCfg.APIBase,
			APIType:  providerCfg.APIType,
			AuthType: providerCfg.AuthType,
			Models:   providerCfg.Models,
		}
	}
	return definitions
}

func buildAgents(
	configs map[string]config.AgentConfig,
	providers *provider.Manager,
	sessionStore session.Store,
) (map[string]*agent.Agent, error) {
	ids := make([]string, 0, len(configs))
	for id := range configs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	agents := make(map[string]*agent.Agent, len(configs))
	for _, id := range ids {
		agentCfg := configs[id]
		current, err := agent.New(agent.Options{
			ID:           id,
			Name:         agentCfg.Name,
			DefaultModel: agentCfg.DefaultModel,
			Models:       agentCfg.Models,
			SoulPath:     agentCfg.SoulPath,
			SessionStore: sessionStore,
		}, providers)
		if err != nil {
			return nil, fmt.Errorf("创建 Agent %q: %w", id, err)
		}
		agents[id] = current
	}
	return agents, nil
}
