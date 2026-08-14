package slack

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/slack-go/slack"

	"github.com/yoshida-rio/marina/internal/storage"
)

// blocksJSON はブロック構成を文字列として検査するためにJSONへ落とします。
func blocksJSON(t *testing.T, blocks []slack.Block) string {
	t.Helper()
	b, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal blocks: %v", err)
	}
	return string(b)
}

// 編集者のHomeタブには編集ボタンが出て、現在の指示と最終更新が表示されること。
func TestHomeBlocksForEditor(t *testing.T) {
	h := NewHomeHandler(nil, nil, "U_ADMIN")
	setting := storage.Setting{
		Name:      "custom_instructions",
		Value:     "SALON BOARD関連のメールは既読にするだけでよい",
		UpdatedBy: "U_ADMIN",
		UpdatedAt: time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC),
	}

	got := blocksJSON(t, h.homeBlocks(setting, "U_ADMIN"))

	if !strings.Contains(got, ActionEditInstructions) {
		t.Error("editor should see the edit button")
	}
	if !strings.Contains(got, "SALON BOARD関連のメールは既読にするだけでよい") {
		t.Error("home should show the current instructions")
	}
	// UTC 01:00 はJSTでは同日10:00。
	if !strings.Contains(got, "2026/08/14 10:00") {
		t.Errorf("home should show the update time in JST, got:\n%s", got)
	}
}

// 編集者以外には編集ボタンを出さず、誰に頼めばよいかを示すこと。
func TestHomeBlocksForNonEditor(t *testing.T) {
	h := NewHomeHandler(nil, nil, "U_ADMIN")

	got := blocksJSON(t, h.homeBlocks(storage.Setting{}, "U_OTHER"))

	if strings.Contains(got, ActionEditInstructions) {
		t.Error("non-editor should not see the edit button")
	}
	if !strings.Contains(got, "U_ADMIN") {
		t.Error("non-editor should be told who can edit")
	}
	if !strings.Contains(got, "まだ設定されていません") {
		t.Error("empty instructions should be shown as unset")
	}
}

// 編集者が未設定なら誰にも編集ボタンを出さないこと。
func TestHomeBlocksWithoutEditorConfigured(t *testing.T) {
	h := NewHomeHandler(nil, nil, "")

	got := blocksJSON(t, h.homeBlocks(storage.Setting{}, "U_ANYONE"))

	if strings.Contains(got, ActionEditInstructions) {
		t.Error("nobody should be able to edit when the editor is not configured")
	}
	if !strings.Contains(got, "HOME_ADMIN_SLACK_USER_ID") {
		t.Error("home should explain why editing is unavailable")
	}
}

func TestCanEdit(t *testing.T) {
	h := NewHomeHandler(nil, nil, "U_ADMIN")
	if !h.CanEdit("U_ADMIN") {
		t.Error("the configured editor should be able to edit")
	}
	if h.CanEdit("U_OTHER") {
		t.Error("other users should not be able to edit")
	}
	if NewHomeHandler(nil, nil, "").CanEdit("") {
		t.Error("an empty user id must never match an unset editor")
	}
}

// モーダルの入力値を取り出せること。未入力・stateなしは空文字になること。
func TestInstructionsFromState(t *testing.T) {
	state := &slack.ViewState{Values: map[string]map[string]slack.BlockAction{
		blockIDInstructions: {actionIDInstructions: {Value: "  経理からのメールは要返信にする  "}},
	}}
	if got := instructionsFromState(state); got != "経理からのメールは要返信にする" {
		t.Errorf("unexpected value: %q", got)
	}
	if got := instructionsFromState(nil); got != "" {
		t.Errorf("nil state should yield an empty value, got %q", got)
	}
	if got := instructionsFromState(&slack.ViewState{}); got != "" {
		t.Errorf("empty state should yield an empty value, got %q", got)
	}
}

// 編集モーダルが現在値を初期値として持ち、空欄でも保存できること。
func TestEditModalView(t *testing.T) {
	view := editModalView("現在の指示")

	if view.CallbackID != CallbackEditInstructions {
		t.Errorf("unexpected callback id: %q", view.CallbackID)
	}
	input, ok := view.Blocks.BlockSet[0].(*slack.InputBlock)
	if !ok {
		t.Fatalf("first block should be an input block, got %T", view.Blocks.BlockSet[0])
	}
	if !input.Optional {
		t.Error("the input must be optional so that clearing the instructions is possible")
	}
	element, ok := input.Element.(*slack.PlainTextInputBlockElement)
	if !ok {
		t.Fatalf("unexpected element type %T", input.Element)
	}
	if element.InitialValue != "現在の指示" {
		t.Errorf("unexpected initial value: %q", element.InitialValue)
	}
	if !element.Multiline {
		t.Error("the input should be multiline")
	}
}
