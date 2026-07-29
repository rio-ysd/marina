// cmd/chatはSlackを介さずターミナルからmarinaのAgentと会話するためのローカル検証用CLIです。
package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/yoshida-rio/marina/internal/app"
	"github.com/yoshida-rio/marina/internal/config"
)

const (
	localChannel = "cli-local"
	localUser    = "cli-user"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	a, err := app.New(cfg)
	if err != nil {
		log.Fatalf("init app: %v", err)
	}
	defer a.DB.Close()

	threadKey := fmt.Sprintf("%s:cli-%d", localChannel, os.Getpid())

	fmt.Println("marina CLIチャット (Ctrl+D または /exit で終了)")
	ctx := context.Background()
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if text == "/exit" {
			break
		}

		reply, err := a.Agent.Respond(ctx, threadKey, localChannel, localUser, text)
		if err != nil {
			fmt.Printf("[error] %v\n", err)
			continue
		}
		fmt.Printf("marina: %s\n", reply)
	}
	if err := scanner.Err(); err != nil {
		log.Fatalf("stdin read error: %v", err)
	}
}
