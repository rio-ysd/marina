package agent

import (
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
)

func toolUseMessage(id, name string) anthropic.BetaMessageParam {
	return anthropic.BetaMessageParam{
		Role: anthropic.BetaMessageParamRoleAssistant,
		Content: []anthropic.BetaContentBlockParamUnion{
			{OfToolUse: &anthropic.BetaToolUseBlockParam{ID: id, Name: name, Input: map[string]any{}}},
		},
	}
}

func toolResultMessage(toolUseID, text string) anthropic.BetaMessageParam {
	return anthropic.BetaMessageParam{
		Role: anthropic.BetaMessageParamRoleUser,
		Content: []anthropic.BetaContentBlockParamUnion{
			{OfToolResult: &anthropic.BetaToolResultBlockParam{
				ToolUseID: toolUseID,
				Content:   []anthropic.BetaToolResultBlockParamContentUnion{{OfText: &anthropic.BetaTextBlockParam{Text: text}}},
			}},
		},
	}
}

func TestStripVerbatimNotice(t *testing.T) {
	text := "重要: そのまま使ってください。\n\n- 藤原香織\n  - なし\n"
	if got := stripVerbatimNotice(text); got != "- 藤原香織\n  - なし\n" {
		t.Errorf("stripVerbatimNotice() = %q", got)
	}
}

func TestStripVerbatimNoticeNoPrefix(t *testing.T) {
	text := "- 藤原香織\n  - なし\n"
	if got := stripVerbatimNotice(text); got != text {
		t.Errorf("stripVerbatimNotice() = %q, want unchanged", got)
	}
}

func TestExtractVerbatimToolResultsMatchesByName(t *testing.T) {
	messages := []anthropic.BetaMessageParam{
		toolUseMessage("id1", "check_staff_attendance_summary"),
		toolResultMessage("id1", "重要: そのまま。\n\n- 藤原香織\n  - 9/4\n"),
		toolUseMessage("id2", "calendar_list_events"),
		toolResultMessage("id2", "予定はありません"),
	}
	got := extractVerbatimToolResults(messages)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0] != "- 藤原香織\n  - 9/4\n" {
		t.Errorf("got[0] = %q", got[0])
	}
}

func TestExtractVerbatimToolResultsEmptyWhenNoMatch(t *testing.T) {
	messages := []anthropic.BetaMessageParam{
		toolUseMessage("id1", "calendar_list_events"),
		toolResultMessage("id1", "予定はありません"),
	}
	if got := extractVerbatimToolResults(messages); len(got) != 0 {
		t.Errorf("expected no verbatim results, got %v", got)
	}
}
