package config

import "testing"

func TestNormalizeEmojiName(t *testing.T) {
	cases := map[string]string{
		":typing-indicator:": "typing-indicator",
		"typing-indicator":   "typing-indicator",
		" :hourglass: ":      "hourglass",
		// off/none/false は無効化(付けない)として扱う
		"off":   "",
		"none":  "",
		"NONE":  "",
		"false": "",
		"":      "",
		"::":    "",
	}
	for in, want := range cases {
		if got := normalizeEmojiName(in); got != want {
			t.Errorf("normalizeEmojiName(%q) = %q, want %q", in, got, want)
		}
	}
}
