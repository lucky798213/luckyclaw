package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"syscall"

	"lukcyclaw/internal/agent"
	"lukcyclaw/internal/bus"
	"lukcyclaw/internal/channels"
	"lukcyclaw/internal/config"
	"lukcyclaw/internal/gateway"
	"lukcyclaw/internal/provider"
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

	agents, err := buildAgents(cfg.Agents, providerManager)
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

	go messageGateway.Run(ctx)
	channelManager.Start(ctx)

	select {
	case <-signalCtx.Done():
	case <-terminal.Done():
	}
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

func buildAgents(configs map[string]config.AgentConfig, providers *provider.Manager) (map[string]*agent.Agent, error) {
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
		}, providers)
		if err != nil {
			return nil, fmt.Errorf("创建 Agent %q: %w", id, err)
		}
		agents[id] = current
	}
	return agents, nil
}
