package morningdigest

import (
	"strings"
	"testing"

	"github.com/yoshida-rio/marina/internal/tools"
)

func TestBuildDigestMessage(t *testing.T) {
	emails := []tools.EmailSummary{
		{ID: "m1", From: "クラウドサイン <noreply@cloudsign.jp>", Subject: "株式譲渡契約書の確認依頼"},
		{ID: "m2", From: "スマートEX", Subject: "ご予約内容の確認"},
	}
	j := &judgement{
		SafeToArchiveIDs: []string{"m2"},
		NeedsReply:       []replyDraft{{MessageID: "m1", Subject: "株式譲渡契約書の確認依頼", DraftBody: "承知しました。"}},
		Summary:          "・クラウドサインから契約書の確認依頼が届いています\n・他は通知・広告のみでした",
	}

	got := buildDigestMessage(emails, j)

	// 1行の長文にならず、見出し・件数・要返信一覧・サマリーが行として分かれていること。
	if lines := strings.Count(got, "\n") + 1; lines < 6 {
		t.Errorf("expected a multi-line digest, got %d lines:\n%s", lines, got)
	}
	for _, want := range []string{
		"朝のメールダイジェスト",
		"未読 2件 / 要返信 1件 / 既読化 1件",
		"*要返信 1件*",
		"• 株式譲渡契約書の確認依頼 — クラウドサイン",
		"<https://mail.google.com/mail/u/0/#all/m1|開く>",
		"*サマリー*",
		"・クラウドサインから契約書の確認依頼が届いています",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("digest missing %q:\n%s", want, got)
		}
	}
	if strings.HasSuffix(got, "\n") {
		t.Error("digest should not end with a newline")
	}
}

func TestBuildDigestMessageWithoutRepliesOmitsSection(t *testing.T) {
	emails := []tools.EmailSummary{{ID: "m1", From: "広告", Subject: "セールのお知らせ"}}
	j := &judgement{SafeToArchiveIDs: []string{"m1"}, Summary: "・対応が必要なメールはありません"}

	got := buildDigestMessage(emails, j)

	if strings.Contains(got, "要返信 1件*") {
		t.Errorf("unexpected needs-reply section:\n%s", got)
	}
	if !strings.Contains(got, "未読 1件 / 要返信 0件 / 既読化 1件") {
		t.Errorf("counts missing:\n%s", got)
	}
}

func TestEscapeMrkdwnAndDisplayName(t *testing.T) {
	// 差出人の山括弧がSlackのリンク記法と衝突しないこと。
	emails := []tools.EmailSummary{{ID: "m1", From: `"佐藤 & 田中" <sato@example.com>`, Subject: "A<B>Cの件"}}
	j := &judgement{NeedsReply: []replyDraft{{MessageID: "m1", Subject: "A<B>Cの件"}}}

	got := buildDigestMessage(emails, j)

	if strings.Contains(got, "<sato@example.com>") {
		t.Errorf("raw angle brackets from the sender leaked into mrkdwn:\n%s", got)
	}
	for _, want := range []string{"佐藤 &amp; 田中", "A&lt;B&gt;Cの件"} {
		if !strings.Contains(got, want) {
			t.Errorf("digest missing escaped %q:\n%s", want, got)
		}
	}
	// 自前で組み立てたリンクはエスケープされず生きていること。
	if !strings.Contains(got, "|開く>") {
		t.Errorf("gmail link was escaped away:\n%s", got)
	}
}

func TestDisplayName(t *testing.T) {
	cases := map[string]string{
		"村田 恵一 <murata@example.co.jp>":      "村田 恵一",
		`"クラウドサイン" <no-reply@cloudsign.jp>`: "クラウドサイン",
		"<only@example.com>":                "only@example.com",
		"plain@example.com":                 "plain@example.com",
		"":                                  "",
	}
	for in, want := range cases {
		if got := displayName(in); got != want {
			t.Errorf("displayName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("  あいうえお  ", 10); got != "あいうえお" {
		t.Errorf("truncate() = %q, want trimmed and unchanged", got)
	}
	if got := truncate("あいうえお", 3); got != "あいう…" {
		t.Errorf("truncate() = %q, want %q", got, "あいう…")
	}
}

// Claudeが一般的なMarkdownで書いてもSlackで太字になること、
// かつ変換で生成したリンクがエスケープで壊れないこと。
func TestBuildDigestMessageConvertsMarkdown(t *testing.T) {
	j := &judgement{Summary: "・**重要**: 請求書の確認依頼\n・[詳細](https://example.com/x)を参照"}

	got := buildDigestMessage([]tools.EmailSummary{{ID: "m1"}}, j)

	if strings.Contains(got, "**重要**") {
		t.Errorf("Markdownの太字が変換されていない:\n%s", got)
	}
	if !strings.Contains(got, "*重要*") {
		t.Errorf("mrkdwnの太字になっていない:\n%s", got)
	}
	if !strings.Contains(got, "<https://example.com/x|詳細>") {
		t.Errorf("リンクが壊れている:\n%s", got)
	}
}
