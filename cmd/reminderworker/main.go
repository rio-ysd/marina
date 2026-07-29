package main

import (
	"context"
	"log"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/yoshida-rio/marina/internal/app"
	"github.com/yoshida-rio/marina/internal/config"
	"github.com/yoshida-rio/marina/internal/reminderworker"
)

var (
	marinaApp *app.App
	worker    *reminderworker.Worker
)

func init() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	marinaApp, err = app.New(cfg)
	if err != nil {
		log.Fatalf("init app: %v", err)
	}
	worker = reminderworker.NewWorker(marinaApp.ReminderRepo, marinaApp.SlackClient)
}

// handler はEventBridge Scheduler(数分おき)からの起動をトリガーとして未送信リマインダーを送信します。
func handler(ctx context.Context) error {
	return worker.RunOnce(ctx)
}

func main() {
	defer marinaApp.DB.Close()
	lambda.Start(handler)
}
