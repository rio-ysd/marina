package agent

import "testing"

// 「好き」と伝えられたら、本人には「大好き」、それ以外には拒否の文言を返すこと。
func TestAffectionReplyDependsOnSpeaker(t *testing.T) {
	if got, ok := affectionReply("<@U0MARINA> 好き", true); !ok || got != "大好き" {
		t.Errorf(`owner should get 大好き, got %q (ok=%v)`, got, ok)
	}
	if got, ok := affectionReply("<@U0MARINA> 好き", false); !ok || got != "セクハラです。やめて下さい" {
		t.Errorf(`others should get the refusal, got %q (ok=%v)`, got, ok)
	}
}

// 好意の表明として拾う言い回し。メンション・絵文字・語尾・句読点の揺れを吸収すること。
func TestIsAffectionMessage(t *testing.T) {
	for _, text := range []string{
		"好き",
		"すき",
		"大好き",
		"だいすき",
		"スキ",
		"好きです",
		"好きだよ",
		"好きだ!",
		"大好きだよ。",
		"好き♡",
		"<@U0MARINA> 好き",
		"<@U0MARINA> 大好き :heart:",
		"marinaのことが好き",
		"marina が 好き",
		"marinaさんが好きです",
		"君が好き",
		"あなたのことが大好きです",
		"　好き　",
	} {
		if !isAffectionMessage(text) {
			t.Errorf("should be treated as affection: %q", text)
		}
	}
}

// 好意の表明ではないもの。用件が混ざるメッセージや質問は通常応答に回すこと。
func TestIsAffectionMessageIgnoresOtherMessages(t *testing.T) {
	for _, text := range []string{
		"",
		"<@U0MARINA>",
		"好きな食べ物は何?",
		"好き?",
		"私のこと好き？",
		"コーヒー好き",
		"この資料好きだけど直したいところがある",
		"好きなだけ休んでいいよと部長に言われた",
		"今日の予定を教えて",
		"すきま時間にやっておく",
	} {
		if isAffectionMessage(text) {
			t.Errorf("should NOT be treated as affection: %q", text)
		}
	}
}
