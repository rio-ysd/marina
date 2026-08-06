package agent

import (
	"context"
	"fmt"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
)

const draftSystemPromptTemplate = `あなたはSlackユーザー「%s」本人の代理で返信文を作成する秘書AIです。
作成した文面は本人の承認後、本人のアカウントから投稿されます。したがって一人称は「%s」本人の視点で書いてください。

守るべきルール:
- 日本語で、Slackでのやりとりに適した簡潔で丁寧な文体にする(過度に硬い定型ビジネスメール調にはしない)
- 事実を推測で断定しない。判断材料が足りない依頼には、確認や折り返しを伝える返信にする
- 期日・金額・合意事項などを勝手に確定させない
- 返信本文のみを出力する。前置き・見出し・「以下が返信案です」等の説明・コードブロックの装飾は書かない`

const maxDraftTokens = 1024

// DraftReply は対象ユーザー宛に届いたメッセージへの返信文の下書きを生成します。
// targetUser/requesterUserは表示名(解決できない場合はユーザーID)、
// threadContextは古い順に並べたスレッドの発言("表示名: 本文"形式)、
// isDMはチャンネルでのメンションではなくDMで届いたメッセージかどうかです。
func (a *Agent) DraftReply(ctx context.Context, targetUser, requesterUser, requestText string, threadContext []string, isDM bool) (string, error) {
	var sb strings.Builder
	if len(threadContext) > 0 {
		sb.WriteString("これまでのスレッドの流れ(古い順):\n")
		for _, line := range threadContext {
			sb.WriteString("- ")
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	if isDM {
		sb.WriteString("これはチャンネルではなく1対1のDMでのやりとりです。第三者は読まないため、その前提で書いてください。\n\n")
	}
	sb.WriteString(fmt.Sprintf("%s さんから %s さん宛に届いたメッセージ:\n", requesterUser, targetUser))
	sb.WriteString(requestText)
	sb.WriteString(fmt.Sprintf("\n\nこのメッセージへの、%s さんとしての返信本文を作成してください。", targetUser))

	msg, err := a.client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:     a.model,
		MaxTokens: maxDraftTokens,
		System: []anthropic.BetaTextBlockParam{{
			Text: fmt.Sprintf(draftSystemPromptTemplate, targetUser, targetUser),
		}},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(sb.String())),
		},
	})
	if err != nil {
		return "", fmt.Errorf("draft reply: %w", err)
	}

	draft := strings.TrimSpace(extractText(msg))
	if draft == "" {
		return "", fmt.Errorf("draft reply: empty response")
	}
	return draft, nil
}
