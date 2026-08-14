// cmd/morningdigest-localはLambdaを介さずローカルから朝のGmailダイジェスト処理
// (不要メールの既読化・要確認メールの抽出・返信下書き作成・Slack通知)を一度だけ実行するCLIです。
package main

import (
	"context"
	"log"

	"github.com/yoshida-rio/marina/internal/app"
	"github.com/yoshida-rio/marina/internal/config"
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

	if !cfg.HasGoogleCredentials() {
		log.Println("警告: GOOGLE_SERVICE_ACCOUNT_JSON/GOOGLE_IMPERSONATED_USERが未設定のため、モックGmailクライアントで実行します。")
	}

	ctx := context.Background()
	if err := a.MorningDigestRunner.Run(ctx); err != nil {
		log.Fatalf("morning digest run error: %v", err)
	}
	log.Println("朝のダイジェスト処理が完了しました。")
}
