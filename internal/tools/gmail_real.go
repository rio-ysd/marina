package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	googleoption "google.golang.org/api/option"
)

// gmailScopes はGmail連携に必要なOAuthスコープです。
var gmailScopes = []string{
	gmail.GmailReadonlyScope,
	gmail.GmailModifyScope,
	gmail.GmailComposeScope,
}

// RealGmailClient はGoogle Workspaceサービスアカウント + ドメイン委任でGmail APIを呼び出す実装です。
type RealGmailClient struct {
	service *gmail.Service
}

// NewRealGmailClient はサービスアカウント鍵JSONと代理対象ユーザーのメールアドレスからGmailClientを構築します。
func NewRealGmailClient(ctx context.Context, serviceAccountJSON, impersonatedUser string) (*RealGmailClient, error) {
	cfg, err := google.JWTConfigFromJSON([]byte(serviceAccountJSON), gmailScopes...)
	if err != nil {
		return nil, fmt.Errorf("parse service account json: %w", err)
	}
	cfg.Subject = impersonatedUser

	svc, err := gmail.NewService(ctx, googleoption.WithHTTPClient(cfg.Client(ctx)))
	if err != nil {
		return nil, fmt.Errorf("create gmail service: %w", err)
	}
	return &RealGmailClient{service: svc}, nil
}

func (c *RealGmailClient) ListUnread(ctx context.Context) ([]EmailSummary, error) {
	list, err := c.service.Users.Messages.List("me").
		Q("is:unread").
		MaxResults(20).
		Context(ctx).
		Do()
	if err != nil {
		return nil, fmt.Errorf("list unread messages: %w", err)
	}

	summaries := make([]EmailSummary, 0, len(list.Messages))
	for _, m := range list.Messages {
		msg, err := c.service.Users.Messages.Get("me", m.Id).
			Format("metadata").
			MetadataHeaders("From", "Subject").
			Context(ctx).
			Do()
		if err != nil {
			return nil, fmt.Errorf("get message %s: %w", m.Id, err)
		}
		summaries = append(summaries, EmailSummary{
			ID:      msg.Id,
			From:    headerValue(msg.Payload.Headers, "From"),
			Subject: headerValue(msg.Payload.Headers, "Subject"),
			Snippet: msg.Snippet,
		})
	}
	return summaries, nil
}

func (c *RealGmailClient) MarkAsRead(ctx context.Context, messageID string) error {
	_, err := c.service.Users.Messages.Modify("me", messageID, &gmail.ModifyMessageRequest{
		RemoveLabelIds: []string{"UNREAD"},
	}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("mark message %s as read: %w", messageID, err)
	}
	return nil
}

func (c *RealGmailClient) CreateDraft(ctx context.Context, messageID, body string) error {
	original, err := c.service.Users.Messages.Get("me", messageID).
		Format("metadata").
		MetadataHeaders("From", "To", "Subject", "Message-ID", "References").
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("get message %s: %w", messageID, err)
	}

	raw, err := buildReplyRaw(original, body)
	if err != nil {
		return fmt.Errorf("build reply for message %s: %w", messageID, err)
	}

	_, err = c.service.Users.Drafts.Create("me", &gmail.Draft{
		Message: &gmail.Message{
			ThreadId: original.ThreadId,
			Raw:      raw,
		},
	}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("create draft for message %s: %w", messageID, err)
	}
	return nil
}

// buildReplyRaw はoriginalメッセージへの返信をRFC 2822形式でエンコードします。
func buildReplyRaw(original *gmail.Message, body string) (string, error) {
	headers := original.Payload.Headers
	to := headerValue(headers, "From")
	subject := headerValue(headers, "Subject")
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	messageID := headerValue(headers, "Message-ID")
	references := strings.TrimSpace(headerValue(headers, "References") + " " + messageID)

	var sb strings.Builder
	fmt.Fprintf(&sb, "To: %s\r\n", to)
	fmt.Fprintf(&sb, "Subject: %s\r\n", subject)
	if messageID != "" {
		fmt.Fprintf(&sb, "In-Reply-To: %s\r\n", messageID)
	}
	if references != "" {
		fmt.Fprintf(&sb, "References: %s\r\n", references)
	}
	sb.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(body)

	return base64.URLEncoding.EncodeToString([]byte(sb.String())), nil
}

func headerValue(headers []*gmail.MessagePartHeader, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}
