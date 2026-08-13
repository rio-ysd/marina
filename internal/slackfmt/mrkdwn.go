// Package slackfmt はClaudeが書く一般的なMarkdownをSlackのmrkdwnへ変換します。
//
// Slackのmrkdwnは一般的なMarkdownと記法が異なり、`**太字**`や`# 見出し`、
// `[text](url)`はそのまま文字として表示されてしまいます。
// LLMの出力は指示だけでは揺れるため、投稿直前に機械的に変換します。
package slackfmt

import (
	"regexp"
	"strings"
)

var (
	// **太字** / __太字__ → *太字*
	boldPattern = regexp.MustCompile(`\*\*([^*\n]+)\*\*|__([^_\n]+)__`)
	// 見出し(# 〜 ######) → *太字*。Slackに見出し記法は無い。
	headingPattern = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+(.+?)\s*$`)
	// [表示文字](URL) → <URL|表示文字>
	linkPattern = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^\s)]+)\)`)
	// 箇条書きの `- ` / `* ` → `• `
	bulletPattern = regexp.MustCompile(`(?m)^(\s*)[-*]\s+`)
	// コードブロックとインラインコードは変換対象外にするため先に切り出す。
	codePattern = regexp.MustCompile("(?s)```.*?```|`[^`\n]*`")
)

// ToMrkdwn はMarkdown記法をSlackのmrkdwnへ変換します。
// コードブロックとインラインコードの中身は変換しません。
func ToMrkdwn(s string) string {
	var b strings.Builder
	last := 0
	for _, loc := range codePattern.FindAllStringIndex(s, -1) {
		b.WriteString(convert(s[last:loc[0]]))
		b.WriteString(s[loc[0]:loc[1]]) // コード部分はそのまま
		last = loc[1]
	}
	b.WriteString(convert(s[last:]))
	return b.String()
}

func convert(s string) string {
	if s == "" {
		return s
	}
	// 太字を先に処理する。`**text**`が行頭にあると箇条書き変換と衝突するため。
	s = boldPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := boldPattern.FindStringSubmatch(m)
		for _, g := range sub[1:] {
			if g != "" {
				return "*" + g + "*"
			}
		}
		return m
	})
	s = headingPattern.ReplaceAllString(s, "*$1*")
	s = linkPattern.ReplaceAllString(s, "<$2|$1>")
	s = bulletPattern.ReplaceAllString(s, "$1• ")
	return s
}
