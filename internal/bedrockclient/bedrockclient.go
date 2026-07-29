// Package bedrockclient はAmazon Bedrock経由でAnthropicクライアントを構築します。
package bedrockclient

import (
	"context"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/bedrock"

	"github.com/yoshida-rio/marina/internal/config"
)

// New はcfgのBedrock設定(リージョン)を使ってanthropic.Clientを構築します。
// 認証はLambdaの実行ロール等、標準のAWSクレデンシャルチェーン(SigV4)を使用します。
func New(ctx context.Context, cfg *config.Config) (anthropic.Client, error) {
	mantle, err := bedrock.NewMantleClient(ctx, bedrock.MantleClientConfig{
		AWSRegion: cfg.BedrockRegion,
	})
	if err != nil {
		return anthropic.Client{}, err
	}

	return anthropic.Client{
		Options:  mantle.Options,
		Messages: anthropic.NewMessageService(mantle.Options...),
		Beta:     anthropic.NewBetaService(mantle.Options...),
	}, nil
}
