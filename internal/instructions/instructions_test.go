package instructions

import (
	"strings"
	"testing"
)

func TestPromptSection(t *testing.T) {
	got := PromptSection("SALON BOARD関連のメールは既読にするだけでよい")
	if !strings.Contains(got, "SALON BOARD関連のメールは既読にするだけでよい") {
		t.Errorf("section should contain the instructions, got %q", got)
	}
	if !strings.HasPrefix(got, "\n\n") {
		t.Errorf("section should start with a blank line so it can be appended as-is, got %q", got)
	}
	if !strings.Contains(got, promptHeading) {
		t.Errorf("section should be introduced by a heading, got %q", got)
	}
}

// 未設定・空白だけの場合は何も足さないこと(既存プロンプトを変えないため)。
func TestPromptSectionEmpty(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\t "} {
		if got := PromptSection(in); got != "" {
			t.Errorf("PromptSection(%q) = %q, want empty", in, got)
		}
	}
}

// 上限を超える指示は保存させないこと(システムプロンプトは全リクエストに乗るため)。
func TestSaveRejectsTooLongText(t *testing.T) {
	store := NewStore(nil)
	err := store.Save(t.Context(), strings.Repeat("あ", MaxLen+1), "U_ADMIN")
	if err == nil {
		t.Fatal("saving text over the limit should fail")
	}
	if !strings.Contains(err.Error(), "文字") {
		t.Errorf("the error should be readable by the user, got %v", err)
	}
}
