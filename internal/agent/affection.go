package agent

import (
	"regexp"
	"strings"
)

// 「好き」と言われたときの返答は固定文言なので、Claudeを呼ばずにここで返します。
// 文言が揺れず、待ち時間もトークン消費もありません。
const (
	// affectionReplyForOwner は本人(PROXY_REPLY_TARGET_USER_ID)への返答です。
	affectionReplyForOwner = "大好き"
	// affectionReplyForOthers は本人以外への返答です。
	affectionReplyForOthers = "セクハラです。やめて下さい"
)

// mentionPattern はメンション(<@U123>)です。
// チャンネルでは「<@U123> 好き」という本文で届くため、判定の前に落とします。
var mentionPattern = regexp.MustCompile(`<@[^>]+>`)

// affectionReply は「好き」だけのメッセージかを判定し、返すべき固定文言を返します。
// それ以外のメッセージは ok=false を返すので、呼び出し側は通常のClaude応答に進めます。
func affectionReply(text string, speakerIsOwner bool) (string, bool) {
	if strings.TrimSpace(mentionPattern.ReplaceAllString(text, "")) != "好き" {
		return "", false
	}
	if speakerIsOwner {
		return affectionReplyForOwner, true
	}
	return affectionReplyForOthers, true
}
