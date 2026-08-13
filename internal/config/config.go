package config

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
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

	// MFClientID/MFClientSecret はMoneyForwardクラウド請求書APIのOAuthクレデンシャル。
	// MFOAuthRedirectURIは認可コールバックのリダイレクトURI。
	// いずれかが未設定の場合はMF請求書ツールはモッククライアントで動作する。
	MFClientID         string
	MFClientSecret     string
	MFOAuthRedirectURI string

	// MorningDigestSlackChannel は朝のダイジェストを送信するSlackチャンネル/DM先ID。
	MorningDigestSlackChannel string

	// SlackUserOAuthToken は代理返信で本人として投稿するためのUser OAuthトークン(xoxp-)。
	// 設定されている場合のみ代理返信フローが有効になる。
	// ProxyReplyTargetUserID は代理返信の対象者のSlackユーザーID。
	// 代理返信できるのはトークンの持ち主本人だけなので、値はトークンの持ち主と一致していなければならない
	// (食い違う場合は起動時に代理返信を無効化する)。未設定ならトークンの持ち主が採用される。
	SlackUserOAuthToken    string
	ProxyReplyTargetUserID string

	// SlackTypingEmoji は処理中であることを示すためにユーザーの発言へ付けるリアクション名。
	// 既定は typing-indicator。"off" または "none" を指定すると付けない。
	// Botトークンに reactions:write スコープが必要。
	SlackTypingEmoji string

	// SlackAppToken はSocket Mode用のApp-Level Token(xapp-、スコープconnections:write)。
	// cmd/socketmodeでのみ使用し、HTTP受信(cmd/server, cmd/lambda)では不要。
	SlackAppToken string
	// ProxyReplyChannelIDs は代理返信を有効にするチャンネルIDの許可リスト(カンマ区切り)。
	// 空の場合はBotが参加している全チャンネルを対象とする。DMには影響しない。
	ProxyReplyChannelIDs []string
	// ProxyReplyIncludeDM は本人へのDMも代理返信の対象にするか(既定: true)。
	// 有効にするにはUser OAuthトークンに im:history スコープが必要。
	ProxyReplyIncludeDM bool
}

// Load は環境変数からConfigを構築します。必須項目が欠けている場合はerrorを返します。
// MARINA_SECRET_ID / DB_SECRET_ID が設定されている場合(Lambda上の想定)は、
// 先にSecrets Managerの値を環境変数へ展開します。未設定なら何もしません。
func Load() (*Config, error) {
	if err := LoadSecretsIntoEnv(context.Background()); err != nil {
		return nil, fmt.Errorf("load secrets: %w", err)
	}

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
		MFOAuthRedirectURI:        os.Getenv("MF_OAUTH_REDIRECT_URI"),
		MorningDigestSlackChannel: os.Getenv("MORNING_DIGEST_SLACK_CHANNEL"),
		SlackUserOAuthToken:       os.Getenv("SLACK_USER_OAUTH_TOKEN"),
		SlackAppToken:             os.Getenv("SLACK_APP_TOKEN"),
		SlackTypingEmoji:          normalizeEmojiName(getEnvDefault("SLACK_TYPING_EMOJI", "typing-indicator")),
		ProxyReplyTargetUserID:    os.Getenv("PROXY_REPLY_TARGET_USER_ID"),
		ProxyReplyChannelIDs:      splitAndTrim(os.Getenv("PROXY_REPLY_CHANNEL_IDS")),
		ProxyReplyIncludeDM:       getEnvBool("PROXY_REPLY_INCLUDE_DM", true),
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
	return c.MFClientID != "" && c.MFClientSecret != "" && c.MFOAuthRedirectURI != ""
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// normalizeEmojiName はリアクション名を正規化します。
// APIはコロンなしの名前を要求するため前後のコロンを外し、off/noneは無効化(空文字)として扱います。
func normalizeEmojiName(v string) string {
	v = strings.Trim(strings.TrimSpace(v), ":")
	switch strings.ToLower(v) {
	case "", "off", "none", "false":
		return ""
	}
	return v
}

// splitAndTrim はカンマ区切りの環境変数値を空要素を除いたスライスにします。
func splitAndTrim(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			result = append(result, p)
		}
	}
	return result
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
