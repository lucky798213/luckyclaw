package main

import (
	"context"
	"log"
	"os"
	"os/signal"
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

	providerCfg, ok := cfg.Providers["deepseek"]
	if !ok {
		log.Fatal("provider deepseek is not configured")
	}

	dsProvider, err := provider.NewOpenAI(providerCfg.APIKey, providerCfg.APIBase)
	if err != nil {
		log.Fatal(err)
	}

	claw, err := agent.New(
		"LuckyClaw",
		"deepseek-chat",
		dsProvider,
		"SOUL.md",
	)
	if err != nil {
		log.Fatal(err)
	}

	messageBus := bus.New()
	messageGateway, err := gateway.New(messageBus, claw)
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
