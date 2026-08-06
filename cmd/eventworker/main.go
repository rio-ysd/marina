// cmd/eventworker は受信Lambda(cmd/lambda)から非同期invokeされ、Slackイベントと
// ボタン押下の実処理(Claude呼び出し・Slackへの投稿)を行うLambdaエントリポイントです。
// 署名検証は受信側で完了しているため、ここでは行いません。
package main

import (
	"context"
	"log"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/yoshida-rio/marina/internal/app"
	"github.com/yoshida-rio/marina/internal/config"
	marinaslack "github.com/yoshida-rio/marina/internal/slack"
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

func handler(_ context.Context, req marinaslack.AsyncRequest) error {
	return marinaApp.HandleAsyncRequest(req)
}

func main() {
	defer marinaApp.DB.Close()
	lambda.Start(handler)
}
