package morningdigest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/slack-go/slack"

	"github.com/yoshida-rio/marina/internal/tools"
)

// judgement はClaudeによるメール判定結果です。BedrockはOutputFormat(構造化出力)に未対応のため、
// プロンプトでJSON形式を指示しレスポンステキストをパースします。
type judgement struct {
	// SafeToArchiveIDs は不要と判断され既読/アーカイブしてよいメールIDの一覧です。
	SafeToArchiveIDs []string `json:"safe_to_archive_ids"`
	// NeedsReply は返信が必要なメールと、その下書き文面です。
	NeedsReply []replyDraft `json:"needs_reply"`
	// Summary はSlackに送る朝の要約テキストです。
	Summary string `json:"summary"`
}

type replyDraft struct {
	MessageID string `json:"message_id"`
	Subject   string `json:"subject"`
	DraftBody string `json:"draft_body" jsonschema:"description=返信下書きの本文(日本語)"`
}

// Runner は朝のGmailダイジェスト処理を実行します。
type Runner struct {
	client       anthropic.Client
	model        anthropic.Model
	gmail        tools.GmailClient
	slackClient  *slack.Client
	slackChannel string
}

func NewRunner(client anthropic.Client, model string, gmail tools.GmailClient, slackClient *slack.Client, slackChannel string) *Runner {
	return &Runner{
		client:       client,
		model:        anthropic.Model(model),
		gmail:        gmail,
		slackClient:  slackClient,
		slackChannel: slackChannel,
	}
}

// Run はGmail未読チェック→不要判定/既読化→返信下書き作成→Slack通知までの一連の処理を実行します。
func (r *Runner) Run(ctx context.Context) error {
	emails, err := r.gmail.ListUnread(ctx)
	if err != nil {
		return fmt.Errorf("list unread emails: %w", err)
	}
	if len(emails) == 0 {
		return r.postSlackSummary(ctx, "本日、未読メールはありませんでした。")
	}

	j, err := r.judge(ctx, emails)
	if err != nil {
		return fmt.Errorf("judge emails: %w", err)
	}

	for _, id := range j.SafeToArchiveIDs {
		if err := r.gmail.MarkAsRead(ctx, id); err != nil {
			return fmt.Errorf("mark as read %s: %w", id, err)
		}
	}
	for _, draft := range j.NeedsReply {
		if err := r.gmail.CreateDraft(ctx, draft.MessageID, draft.DraftBody); err != nil {
			return fmt.Errorf("create draft %s: %w", draft.MessageID, err)
		}
	}

	return r.postSlackSummary(ctx, j.Summary)
}

const judgementJSONSchema = `{
  "safe_to_archive_ids": ["不要と判断し既読にしてよいメールIDの一覧"],
  "needs_reply": [{"message_id": "...", "subject": "...", "draft_body": "返信下書きの本文(日本語)"}],
  "summary": "Slackに送信する朝のダイジェスト要約(日本語)"
}`

func (r *Runner) judge(ctx context.Context, emails []tools.EmailSummary) (*judgement, error) {
	var sb strings.Builder
	sb.WriteString("以下は未読メールの一覧です。それぞれについて「既読にしてよい不要なメールか」「返信が必要か」を判断してください。\n")
	sb.WriteString("返信が必要なものには、丁寧で簡潔な日本語の返信下書きを作成してください。\n\n")
	for _, e := range emails {
		sb.WriteString(fmt.Sprintf("- id: %s\n  from: %s\n  subject: %s\n  snippet: %s\n", e.ID, e.From, e.Subject, e.Snippet))
	}
	sb.WriteString("\n以下のJSON形式のみで出力してください。説明文やコードブロックの装飾は不要です。\n")
	sb.WriteString(judgementJSONSchema)

	msg, err := r.client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:     r.model,
		MaxTokens: 4096,
		System: []anthropic.BetaTextBlockParam{{
			Text: "あなたは秘書AIエージェント「marina」です。ユーザーの代わりにメールの要否判断と返信下書き作成を行います。指定されたJSON形式のみで応答してください。",
		}},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(sb.String())),
		},
	})
	if err != nil {
		return nil, err
	}

	var result judgement
	if err := json.Unmarshal([]byte(extractJSON(extractText(msg))), &result); err != nil {
		return nil, fmt.Errorf("parse output: %w", err)
	}
	return &result, nil
}

func extractText(msg *anthropic.BetaMessage) string {
	var b strings.Builder
	for _, c := range msg.Content {
		if tb, ok := c.AsAny().(anthropic.BetaTextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return b.String()
}

// extractJSON はコードブロック(```json ... ```)等で囲まれていても最初の{}ブロックを取り出します。
func extractJSON(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start == -1 || end == -1 || end < start {
		return s
	}
	return s[start : end+1]
}

func (r *Runner) postSlackSummary(ctx context.Context, summary string) error {
	_, _, err := r.slackClient.PostMessageContext(ctx, r.slackChannel, slack.MsgOptionText(summary, false))
	return err
}
