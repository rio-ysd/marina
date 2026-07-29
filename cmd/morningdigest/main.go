package main

import (
	"context"
	"log"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/yoshida-rio/marina/internal/app"
	"github.com/yoshida-rio/marina/internal/config"
)

var marinaApp *app.App

func init() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	marinaApp, err = app.New(cfg)
	if err != nil {
		log.Fatalf("init app: %v", err)
	}
}

// handler はEventBridge Scheduler(毎朝9時)からの起動をトリガーとして朝のGmailダイジェストを実行します。
func handler(ctx context.Context) error {
	return marinaApp.MorningDigestRunner.Run(ctx)
}

func main() {
	defer marinaApp.DB.Close()
	lambda.Start(handler)
}
