package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/toolrunner"
)

// EmailSummary はGmailメールの要約情報です。
type EmailSummary struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	Subject string `json:"subject"`
	Snippet string `json:"snippet"`
}

// GmailClient はGmail連携に必要な操作を抽象化するインターフェースです。
// 本実装(Google Workspaceサービスアカウント+ドメイン委任)は認証情報確定後に用意し、
// 未確定の間はMockGmailClientで疎通確認を行います。
type GmailClient interface {
	// ListUnread は未読メールの一覧を返します。
	ListUnread(ctx context.Context) ([]EmailSummary, error)
	// MarkAsRead は指定したメールを既読にします。
	MarkAsRead(ctx context.Context, messageID string) error
	// CreateDraft は指定したメールへの返信下書きを作成します。
	CreateDraft(ctx context.Context, messageID, body string) error
}

// MockGmailClient はGoogle認証情報が未設定の間に使うスタブ実装です。
type MockGmailClient struct{}

func NewMockGmailClient() *MockGmailClient { return &MockGmailClient{} }

func (m *MockGmailClient) ListUnread(ctx context.Context) ([]EmailSummary, error) {
	log.Println("[MockGmailClient] ListUnread called (Gmail認証情報が未設定のためモック応答を返します)")
	return []EmailSummary{
		{ID: "mock-1", From: "sample@example.com", Subject: "[サンプル] 見積のご相談", Snippet: "これはGmail連携未設定時のモックデータです。"},
	}, nil
}

func (m *MockGmailClient) MarkAsRead(ctx context.Context, messageID string) error {
	log.Printf("[MockGmailClient] MarkAsRead(%s) called (モック)", messageID)
	return nil
}

func (m *MockGmailClient) CreateDraft(ctx context.Context, messageID, body string) error {
	log.Printf("[MockGmailClient] CreateDraft(%s) called (モック): %s", messageID, body)
	return nil
}

// NewGmailTools はClaudeのFunction CallingからGmailClientを操作するためのBetaToolセットを構築します。
func NewGmailTools(client GmailClient) ([]anthropic.BetaTool, error) {
	listTool, err := toolrunner.NewBetaToolFromBytes[struct{}](
		"gmail_list_unread",
		"Gmailの未読メール一覧を取得する。",
		mustSchema(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		func(ctx context.Context, _ struct{}) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			emails, err := client.ListUnread(ctx)
			if err != nil {
				return textResult(""), err
			}
			b, err := json.Marshal(emails)
			if err != nil {
				return textResult(""), err
			}
			return textResult(string(b)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	markReadTool, err := toolrunner.NewBetaToolFromBytes[markReadInput](
		"gmail_mark_as_read",
		"指定したメールIDを既読にする。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message_id": map[string]any{"type": "string"},
			},
			"required": []string{"message_id"},
		}),
		func(ctx context.Context, in markReadInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			if err := client.MarkAsRead(ctx, in.MessageID); err != nil {
				return textResult(""), err
			}
			return textResult(fmt.Sprintf("メール(%s)を既読にしました。", in.MessageID)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	draftTool, err := toolrunner.NewBetaToolFromBytes[createDraftInput](
		"gmail_create_draft",
		"指定したメールへの返信下書きを作成する。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message_id": map[string]any{"type": "string"},
				"body":       map[string]any{"type": "string", "description": "返信本文"},
			},
			"required": []string{"message_id", "body"},
		}),
		func(ctx context.Context, in createDraftInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			if err := client.CreateDraft(ctx, in.MessageID, in.Body); err != nil {
				return textResult(""), err
			}
			return textResult(fmt.Sprintf("メール(%s)への返信下書きを作成しました。", in.MessageID)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	return []anthropic.BetaTool{listTool, markReadTool, draftTool}, nil
}

type markReadInput struct {
	MessageID string `json:"message_id"`
}

type createDraftInput struct {
	MessageID string `json:"message_id"`
	Body      string `json:"body"`
}
