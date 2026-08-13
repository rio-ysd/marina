package slackfmt

import "testing"

func TestToMrkdwn(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"太字", "合計は**12,993,955円**です", "合計は*12,993,955円*です"},
		{"アンダースコアの太字", "__重要__な件", "*重要*な件"},
		{"見出し", "## 今月の請求書\n本文", "*今月の請求書*\n本文"},
		{"リンク", "[請求書](https://invoice.moneyforward.com/x)を確認",
			"<https://invoice.moneyforward.com/x|請求書>を確認"},
		{"箇条書き(ハイフン)", "- 1件目\n- 2件目", "• 1件目\n• 2件目"},
		{"箇条書き(アスタリスク)", "* 1件目\n* 2件目", "• 1件目\n• 2件目"},
		{"インデントされた箇条書き", "  - 子項目", "  • 子項目"},
		{"行頭の太字は箇条書きにしない", "**注意**: 未入金です", "*注意*: 未入金です"},
		{"既にmrkdwnの太字はそのまま", "*太字*はそのまま", "*太字*はそのまま"},
		{"変換対象なし", "普通の文章です。", "普通の文章です。"},
		{"空文字", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ToMrkdwn(c.in); got != c.want {
				t.Errorf("ToMrkdwn(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// コード中のMarkdownは意味が変わるため変換しない。
func TestToMrkdwnKeepsCodeUntouched(t *testing.T) {
	cases := []struct{ name, in string }{
		{"コードブロック", "```\n**bold** と - list\n```"},
		{"言語指定つき", "```go\nfmt.Println(\"**x**\")\n```"},
		{"インラインコード", "`**bold**` は変換しない"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ToMrkdwn(c.in); got != c.in {
				t.Errorf("ToMrkdwn(%q) = %q, want unchanged", c.in, got)
			}
		})
	}
}

// コードブロックの外側は通常どおり変換されること。
func TestToMrkdwnConvertsAroundCode(t *testing.T) {
	in := "**前**\n```\n**中**\n```\n**後**"
	want := "*前*\n```\n**中**\n```\n*後*"
	if got := ToMrkdwn(in); got != want {
		t.Errorf("ToMrkdwn() = %q, want %q", got, want)
	}
}
