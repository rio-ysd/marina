package agent

import (
	"regexp"
	"strings"
)

// 「好き」と好意を伝えられたときの返答は固定文言なので、Claudeを呼ばずにここの判定だけで返します。
// 文言が揺れず、応答も即座に返り、トークンも消費しません。
// ここで拾えない言い回し(「ずっと前から好きでした」等)は、
// システムプロンプトに載せた同じルール(affectionRuleForOwner/affectionRuleForOthers)が受け止めます。

const (
	// affectionReplyForOwner は本人(PROXY_REPLY_TARGET_USER_ID)への返答です。
	affectionReplyForOwner = "大好き"
	// affectionReplyForOthers は本人以外への返答です。
	affectionReplyForOthers = "セクハラです。やめて下さい"
)

// mentionOrEmojiPattern はメンション(<@U123>)・リンク・絵文字ショートコード(:heart:)にマッチします。
// 「@marina 好き :heart:」のような発言も素の「好き」として扱うために落とします。
var mentionOrEmojiPattern = regexp.MustCompile(`<[^>]*>|:[a-z0-9_+-]+:`)

// affectionCores は「好き」そのものの表記ゆれです。ここに完全一致したときだけ好意の表明と見なします。
var affectionCores = map[string]bool{
	"好き":   true,
	"大好き":  true,
	"すき":   true,
	"だいすき": true,
	"スキ":   true,
	"ダイスキ": true,
}

// affectionPrefixes は「好き」の前に付きうる宛先・主題の言い回しです。
// 「marinaのことが好き」のように重なるため、落とせなくなるまで繰り返し削ります。
var affectionPrefixes = []string{
	"marina", "まりな", "マリナ", "さん", "ちゃん",
	"君", "きみ", "あなた", "お前", "おまえ",
	"のこと", "こと", "が", "は", "も", "って", "、", ",",
}

// affectionSuffixes は「好き」の後に付きうる語尾です。
var affectionSuffixes = []string{
	"です", "ます", "だよ", "だぜ", "だな", "だ", "なの", "なんだ", "かも", "よ", "ね", "わ",
}

// affectionTrimCutset は文末の飾り(句読点・記号)です。空白はFieldsで先に落とします。
const affectionTrimCutset = "。.．!！~〜ー…♪♡❤💕★☆"

// affectionReply は好意を伝えるだけのメッセージかを判定し、返すべき固定文言を返します。
// 好意の表明でない場合は ok=false を返すので、呼び出し側は通常のClaude応答に進めます。
func affectionReply(text string, speakerIsOwner bool) (string, bool) {
	if !isAffectionMessage(text) {
		return "", false
	}
	if speakerIsOwner {
		return affectionReplyForOwner, true
	}
	return affectionReplyForOthers, true
}

// isAffectionMessage は「好き」と伝えるだけのメッセージかを判定します。
// 「好きな食べ物は?」のように好意以外の用件が混ざるものは、確実に区別できるよう対象外にします。
func isAffectionMessage(text string) bool {
	s := strings.ToLower(mentionOrEmojiPattern.ReplaceAllString(text, ""))
	// 「好き?」は好意の表明ではなく質問なので、Claudeの通常応答に任せる。
	if strings.ContainsAny(s, "?？") {
		return false
	}
	// 「marina が 好き」のように分かち書きされても拾えるよう、空白は全て落とす。
	s = strings.Join(strings.Fields(s), "")
	s = strings.Trim(s, affectionTrimCutset)
	s = trimAffixes(s, affectionPrefixes, true)
	s = trimAffixes(s, affectionSuffixes, false)
	return affectionCores[s]
}

// trimAffixes はaffixesに一致する接頭辞(prefix=true)または接尾辞を、これ以上落とせなくなるまで削ります。
func trimAffixes(s string, affixes []string, prefix bool) string {
	for {
		trimmed := s
		for _, a := range affixes {
			if prefix {
				trimmed = strings.TrimPrefix(trimmed, a)
			} else {
				trimmed = strings.TrimSuffix(trimmed, a)
			}
		}
		if trimmed == s {
			return s
		}
		s = trimmed
	}
}
