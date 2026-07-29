package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config は marina の実行に必要な環境変数をまとめたものです。
type Config struct {
	AnthropicModel     string
	SlackBotToken      string
	SlackSigningSecret string
	DBDSN              string

	// BedrockRegion はClaudeモデルを呼び出すAWSリージョン(Amazon Bedrock経由)。
	// 認証はLambda実行ロール等、標準のAWSクレデンシャルチェーン(SigV4)を使用する。
	BedrockRegion string

	// GoogleServiceAccountJSON はサービスアカウントの秘密鍵JSON文字列。
	// 未設定の場合はGmail連携はモッククライアントで動作する。
	GoogleServiceAccountJSON string
	// GoogleImpersonatedUser はドメイン委任で代理するユーザーのメールアドレス。
	GoogleImpersonatedUser string

	// MFClientID/MFClientSecret はMoneyForwardクラウド会計APIのOAuthクレデンシャル。
	// 未設定の場合はMF請求書ツールはモッククライアントで動作する。
	MFClientID     string
	MFClientSecret string

	// MorningDigestSlackChannel は朝のダイジェストを送信するSlackチャンネル/DM先ID。
	MorningDigestSlackChannel string
}

// Load は環境変数からConfigを構築します。必須項目が欠けている場合はerrorを返します。
func Load() (*Config, error) {
	cfg := &Config{
		AnthropicModel:            getEnvDefault("ANTHROPIC_MODEL", "anthropic.claude-sonnet-5"),
		SlackBotToken:             os.Getenv("SLACK_BOT_TOKEN"),
		SlackSigningSecret:        os.Getenv("SLACK_SIGNING_SECRET"),
		DBDSN:                     os.Getenv("DB_DSN"),
		BedrockRegion:             getEnvDefault("BEDROCK_REGION", "us-east-1"),
		GoogleServiceAccountJSON:  os.Getenv("GOOGLE_SERVICE_ACCOUNT_JSON"),
		GoogleImpersonatedUser:    os.Getenv("GOOGLE_IMPERSONATED_USER"),
		MFClientID:                os.Getenv("MF_CLIENT_ID"),
		MFClientSecret:            os.Getenv("MF_CLIENT_SECRET"),
		MorningDigestSlackChannel: os.Getenv("MORNING_DIGEST_SLACK_CHANNEL"),
	}

	required := map[string]string{
		"SLACK_BOT_TOKEN":      cfg.SlackBotToken,
		"SLACK_SIGNING_SECRET": cfg.SlackSigningSecret,
		"DB_DSN":               cfg.DBDSN,
	}
	for name, v := range required {
		if v == "" {
			return nil, fmt.Errorf("required environment variable %s is not set", name)
		}
	}

	return cfg, nil
}

// HasGmailCredentials はGmail連携の実クライアントが利用可能かを返します。
func (c *Config) HasGmailCredentials() bool {
	return c.GoogleServiceAccountJSON != "" && c.GoogleImpersonatedUser != ""
}

// HasMFCredentials はMoneyForward請求書APIの実クライアントが利用可能かを返します。
func (c *Config) HasMFCredentials() bool {
	return c.MFClientID != "" && c.MFClientSecret != ""
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
