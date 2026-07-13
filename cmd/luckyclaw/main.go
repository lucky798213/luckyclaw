package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"lukcyclaw/internal/agent"
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

	claw, err := agent.New(
		"LuckyClaw",
		"deepseek-chat",
		dsProvider,
		"SOUL.md",
	)
	if err != nil {
		log.Fatal(err)
	}

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Printf("当前 Session: %s\n", claw.SessionKey())
	fmt.Println("输入 /new 开始新会话，按 Ctrl+D 退出")
	fmt.Print("> ")
	for scanner.Scan() {
		input := scanner.Text()
		if strings.TrimSpace(input) == "/new" {
			claw.Reset()
			fmt.Printf("新会话已开始: %s\n", claw.SessionKey())
			fmt.Print("> ")
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		message, err := claw.Chat(ctx, input)
		cancel()
		if err != nil {
			log.Printf("chat: %v", err)
			fmt.Print("> ")
			continue
		}

		fmt.Println(message.Content)
		fmt.Print("> ")
	}
}
