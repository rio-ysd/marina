package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
)

// SDKのContentBlockUnionは生JSONから変換されるため、構造体を直接組み立てず
// APIレスポンスと同じJSONからデコードする。
func decodeMessage(t *testing.T, raw string) *anthropic.BetaMessage {
	t.Helper()
	var msg anthropic.BetaMessage
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	return &msg
}

func textMessage(t *testing.T, text string) *anthropic.BetaMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"stop_reason": "end_turn",
		"content":     []any{map[string]any{"type": "text", "text": text}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return decodeMessage(t, string(raw))
}

func TestReplyOrFallbackReturnsText(t *testing.T) {
	if got := replyOrFallback(textMessage(t, "今月の請求書は5通です。")); got != "今月の請求書は5通です。" {
		t.Errorf("replyOrFallback() = %q", got)
	}
}

// 本文が空のままSlackへ投稿するとno_textエラーで送信が失敗し、ユーザーには何も届かない。
func TestReplyOrFallbackNeverReturnsEmpty(t *testing.T) {
	cases := map[string]*anthropic.BetaMessage{
		"nil":      nil,
		"blocksなし": decodeMessage(t, `{"stop_reason":"end_turn","content":[]}`),
		"空白のみ":     textMessage(t, "   \n  "),
		"ツール実行中で打ち切られた": decodeMessage(t, `{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"mf_list_invoices","input":{}}]}`),
	}
	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			if got := strings.TrimSpace(replyOrFallback(msg)); got == "" {
				t.Error("replyOrFallback() returned empty text")
			}
		})
	}
}

// 反復上限で打ち切られた場合は、原因が分かる文面を返す。
func TestReplyOrFallbackExplainsToolLimit(t *testing.T) {
	got := replyOrFallback(decodeMessage(t, `{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"mf_list_invoices","input":{}}]}`))
	if !strings.Contains(got, "上限") {
		t.Errorf("fallback should mention the limit: %q", got)
	}
}

// 「今月」「来月」を解決できるよう、システムプロンプトに現在日付(JST)が入ること。
func TestSystemPromptIncludesJSTDate(t *testing.T) {
	// UTCでは前日になる時刻。JSTで解釈されていれば8月14日になる。
	utcNight := time.Date(2026, 8, 13, 20, 30, 0, 0, time.UTC)

	got := systemPromptWithDate(utcNight, "")

	if !strings.Contains(got, "2026年8月14日") {
		t.Errorf("prompt should carry the JST date, got:\n%s", got)
	}
	if !strings.Contains(got, "marina") {
		t.Error("prompt should still contain the base system prompt")
	}
}

// App Homeで設定した追加指示がシステムプロンプトに載ること。未設定なら何も足さないこと。
func TestSystemPromptIncludesCustomInstructions(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)

	got := systemPromptWithDate(now, "SALON BOARD関連のメールは既読にするだけでよい")
	if !strings.Contains(got, "SALON BOARD関連のメールは既読にするだけでよい") {
		t.Errorf("prompt should carry the custom instructions, got:\n%s", got)
	}

	withoutCustom := systemPromptWithDate(now, "   ")
	if strings.Contains(withoutCustom, "追加の運用ルール") {
		t.Errorf("blank instructions should add nothing, got:\n%s", withoutCustom)
	}
}
