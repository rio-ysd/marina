package morningdigest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/slack-go/slack"

	"github.com/yoshida-rio/marina/internal/slackfmt"
	"github.com/yoshida-rio/marina/internal/tools"
)

// Slackメッセージ整形用の上限と、Gmailの該当メールを開くURLのプレフィックス。
const (
	maxSubjectLen         = 60
	maxFromLen            = 30
	gmailMessageURLPrefix = "https://mail.google.com/mail/u/0/#all/"
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
		return r.postSlack(ctx, ":sunny: *朝のメールダイジェスト*\n本日、未読メールはありませんでした。")
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

	return r.postSlack(ctx, buildDigestMessage(emails, j))
}

// buildDigestMessage はSlackに投稿するダイジェスト本文を組み立てます。
// Claudeのsummaryをそのまま流すと1行の長文になって読みにくいため、
// 件数の内訳と要返信メールの一覧をGo側で構造化して整形します。
func buildDigestMessage(emails []tools.EmailSummary, j *judgement) string {
	fromByID := make(map[string]string, len(emails))
	for _, e := range emails {
		fromByID[e.ID] = e.From
	}

	var b strings.Builder
	fmt.Fprintf(&b, ":sunny: *朝のメールダイジェスト*\n未読 %d件 / 要返信 %d件 / 既読化 %d件\n",
		len(emails), len(j.NeedsReply), len(j.SafeToArchiveIDs))

	if len(j.NeedsReply) > 0 {
		fmt.Fprintf(&b, "\n*要返信 %d件*\n", len(j.NeedsReply))
		for _, d := range j.NeedsReply {
			line := fmt.Sprintf("• %s", escapeMrkdwn(truncate(d.Subject, maxSubjectLen)))
			if from := displayName(fromByID[d.MessageID]); from != "" {
				line += fmt.Sprintf(" — %s", escapeMrkdwn(truncate(from, maxFromLen)))
			}
			if d.MessageID != "" {
				line += fmt.Sprintf(" <%s%s|開く>", gmailMessageURLPrefix, d.MessageID)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("_返信下書きはGmailに作成済みです。_\n")
	}

	if summary := strings.TrimSpace(j.Summary); summary != "" {
		b.WriteString("\n*サマリー*\n")
		// 先にエスケープしてからmrkdwnへ変換する。順序が逆だと、変換で作った
		// リンク(<url|text>)の山括弧までエスケープされて表示が壊れる。
		b.WriteString(slackfmt.ToMrkdwn(escapeMrkdwn(summary)))
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// escapeMrkdwn はSlackがリンクやエンティティとして解釈する文字をエスケープします。
// ダイジェストはmrkdwnとして投稿するため、メールの件名や差出人に含まれる山括弧
// (例: `名前 <addr@example.com>`)がリンク記法と衝突して表示が壊れるのを防ぎます。
func escapeMrkdwn(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return strings.ReplaceAll(s, ">", "&gt;")
}

// displayName は`名前 <addr@example.com>`形式の差出人から表示名だけを取り出します。
// 表示名が無い場合はアドレスをそのまま返します。
func displayName(from string) string {
	from = strings.TrimSpace(from)
	if i := strings.Index(from, "<"); i > 0 {
		if name := strings.Trim(strings.TrimSpace(from[:i]), `"'`); name != "" {
			return name
		}
	}
	return strings.Trim(from, "<>")
}

func truncate(s string, limit int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}

const judgementJSONSchema = `{
  "safe_to_archive_ids": ["不要と判断し既読にしてよいメールIDの一覧"],
  "needs_reply": [{"message_id": "...", "subject": "...", "draft_body": "返信下書きの本文(日本語)"}],
  "summary": "Slackに送信する朝のダイジェスト要約(日本語)。1トピック1行の箇条書きにし、各行を「・」で始めて改行(\\n)で区切る"
}`

func (r *Runner) judge(ctx context.Context, emails []tools.EmailSummary) (*judgement, error) {
	var sb strings.Builder
	sb.WriteString("以下は未読メールの一覧です。それぞれについて「既読にしてよい不要なメールか」「返信が必要か」を判断してください。\n")
	sb.WriteString("返信が必要なものには、丁寧で簡潔な日本語の返信下書きを作成してください。\n\n")
	for _, e := range emails {
		sb.WriteString(fmt.Sprintf("- id: %s\n  from: %s\n  subject: %s\n  snippet: %s\n", e.ID, e.From, e.Subject, e.Snippet))
	}
	sb.WriteString("\nsummaryは件数の羅列ではなく、その日に把握しておくべきことを1トピック1行で簡潔に書いてください。\n")
	sb.WriteString("以下のJSON形式のみで出力してください。説明文やコードブロックの装飾は不要です。\n")
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

// postSlack はダイジェストをSlackへ投稿します。mrkdwnとして解釈させるためエスケープしません。
func (r *Runner) postSlack(ctx context.Context, text string) error {
	_, _, err := r.slackClient.PostMessageContext(ctx, r.slackChannel, slack.MsgOptionText(text, false))
	return err
}
