package main

import (
	"context"
	"log"
	"time"

	"lukcyclaw/internal/config"
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	message, err := dsProvider.Chat(
		ctx,
		[]provider.Message{
			{
				Role:    "user",
				Content: "你好，请用一句话介绍你自己。",
			},
		},
		nil,
		"deepseek-chat",
		1024,
		0.7,
	)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("assistant: %s", message.Content)
}
