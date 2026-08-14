package agent

import "testing"

// 「好き」と言われたら、本人には「大好き」、それ以外には拒否の文言を返すこと。
func TestAffectionReplyDependsOnSpeaker(t *testing.T) {
	if got, ok := affectionReply("<@U0MARINA> 好き", true); !ok || got != "大好き" {
		t.Errorf(`owner should get 大好き, got %q (ok=%v)`, got, ok)
	}
	if got, ok := affectionReply("好き", false); !ok || got != "セクハラです。やめて下さい" {
		t.Errorf(`others should get the refusal, got %q (ok=%v)`, got, ok)
	}
}

// 「好き」以外のメッセージは通常のClaude応答に回すこと。
func TestAffectionReplyIgnoresOtherMessages(t *testing.T) {
	for _, text := range []string{
		"",
		"<@U0MARINA>",
		"大好き",
		"好きです",
		"好き?",
		"好きな食べ物は何?",
		"コーヒー好き",
		"今日の予定を教えて",
	} {
		if _, ok := affectionReply(text, false); ok {
			t.Errorf("should not be treated as affection: %q", text)
		}
	}
}
