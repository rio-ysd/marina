package proxyreply

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

const targetUser = "U05RNEMF1CZ"

// newTestService はテスト用のServiceを構築します。
// userClientはHandlesの有効判定にのみ使うため、実際のAPI呼び出しは行いません。
func newTestService(cfg Config) *Service {
	cfg.TargetUserID = targetUser
	return NewService(cfg, nil, nil, nil, &slack.Client{})
}

func TestHandles(t *testing.T) {
	tests := []struct {
		name    string
		service *Service
		msg     IncomingMessage
		want    bool
	}{
		{
			name:    "チャンネルでの対象ユーザー宛メンションは処理する",
			service: newTestService(Config{}),
			msg:     IncomingMessage{Channel: "C111", User: "U999", Text: "<@" + targetUser + "> 見積書の件どうなってますか?"},
			want:    true,
		},
		{
			name:    "チャンネルでメンションがない場合は処理しない",
			service: newTestService(Config{}),
			msg:     IncomingMessage{Channel: "C111", User: "U999", Text: "今日は暑いですね"},
			want:    false,
		},
		{
			name:    "対象ユーザー本人の発言は処理しない",
			service: newTestService(Config{}),
			msg:     IncomingMessage{Channel: "C111", User: targetUser, Text: "<@" + targetUser + "> 自分宛のメモ"},
			want:    false,
		},
		{
			name:    "許可リストに含まれるチャンネルは処理する",
			service: newTestService(Config{AllowedChannels: []string{"C111", "C222"}}),
			msg:     IncomingMessage{Channel: "C222", User: "U999", Text: "<@" + targetUser + "> お願いします"},
			want:    true,
		},
		{
			name:    "許可リスト外のチャンネルは処理しない",
			service: newTestService(Config{AllowedChannels: []string{"C111"}}),
			msg:     IncomingMessage{Channel: "C333", User: "U999", Text: "<@" + targetUser + "> お願いします"},
			want:    false,
		},
		{
			name:    "DMはメンションがなくても処理する",
			service: newTestService(Config{IncludeDM: true}),
			msg:     IncomingMessage{Channel: "D111", User: "U999", Text: "来週の打ち合わせ何時からでしたっけ?", IsDM: true},
			want:    true,
		},
		{
			name:    "DMは許可リストの対象外(チャンネル許可リストがあってもDMは処理する)",
			service: newTestService(Config{AllowedChannels: []string{"C111"}, IncludeDM: true}),
			msg:     IncomingMessage{Channel: "D111", User: "U999", Text: "確認お願いします", IsDM: true},
			want:    true,
		},
		{
			name:    "IncludeDMが無効ならDMは処理しない",
			service: newTestService(Config{IncludeDM: false}),
			msg:     IncomingMessage{Channel: "D111", User: "U999", Text: "確認お願いします", IsDM: true},
			want:    false,
		},
		{
			name:    "本人が送ったDMは処理しない",
			service: newTestService(Config{IncludeDM: true}),
			msg:     IncomingMessage{Channel: "D111", User: targetUser, Text: "了解です", IsDM: true},
			want:    false,
		},
		{
			name:    "User OAuthトークン未設定なら処理しない",
			service: NewService(Config{TargetUserID: targetUser, IncludeDM: true}, nil, nil, nil, nil),
			msg:     IncomingMessage{Channel: "C111", User: "U999", Text: "<@" + targetUser + "> お願いします"},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.service.Handles(tt.msg); got != tt.want {
				t.Errorf("Handles() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveMentions(t *testing.T) {
	s := newTestService(Config{})
	// 表示名をキャッシュに載せ、Slack APIを呼ばずに解決させます。
	s.userNames.Store(targetUser, "rio")
	s.userNames.Store("U999", "tanaka")

	got := s.resolveMentions(context.Background(), "<@"+targetUser+"> <@U999> さんの件、確認お願いします")
	want := "@rio @tanaka さんの件、確認お願いします"
	if got != want {
		t.Errorf("resolveMentions() = %q, want %q", got, want)
	}
}

func TestIsDMChannel(t *testing.T) {
	if !isDMChannel("D0A2ZTZ4WAY") {
		t.Error("isDMChannel(D...) = false, want true")
	}
	for _, id := range []string{"C0A2ZTZ4WAY", "G0A2ZTZ4WAY", ""} {
		if isDMChannel(id) {
			t.Errorf("isDMChannel(%q) = true, want false", id)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("あいうえお", 10); got != "あいうえお" {
		t.Errorf("truncate() = %q, want unchanged", got)
	}
	got := truncate("あいうえお", 3)
	if !strings.HasPrefix(got, "あいう") || !strings.HasSuffix(got, "(以下省略)") {
		t.Errorf("truncate() = %q, want truncated at 3 runes", got)
	}
}

func TestBlockquote(t *testing.T) {
	if got := blockquote("1行目\n2行目"); got != "> 1行目\n> 2行目" {
		t.Errorf("blockquote() = %q", got)
	}
}

func TestApprovalBlocksIncludesPermalink(t *testing.T) {
	s := newTestService(Config{})
	blocks := s.approvalBlocks(approvalView{
		DraftID:       42,
		RequesterUser: "U999",
		Channel:       "C111",
		RequestText:   "見積書の件どうなってますか?",
		Draft:         "確認して本日中にお送りします。",
		Permalink:     "https://devenue.slack.com/archives/C111/p1786500179076929",
	})

	rendered := renderBlocks(t, blocks)
	for _, want := range []string{
		"<https://devenue.slack.com/archives/C111/p1786500179076929|Slackで開く>",
		"*元のメッセージ*",
		"> 見積書の件どうなってますか?",
		"*返信案*",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("approval message missing %q:\n%s", want, rendered)
		}
	}
}

func TestApprovalBlocksWithoutPermalink(t *testing.T) {
	s := newTestService(Config{})
	blocks := s.approvalBlocks(approvalView{DraftID: 1, RequesterUser: "U999", Channel: "C111", RequestText: "本文", Draft: "返信案"})

	rendered := renderBlocks(t, blocks)
	// リンクが取れなかった場合は見出しだけを出し、空リンクを描画しないこと。
	if strings.Contains(rendered, "|Slackで開く>") {
		t.Errorf("unexpected empty permalink link:\n%s", rendered)
	}
	if !strings.Contains(rendered, `*元のメッセージ*\n> 本文`) {
		t.Errorf("original message section malformed:\n%s", rendered)
	}
}

// renderBlocks はBlock KitをJSONに落として文字列として検証できるようにします。
// SetEscapeHTML(false)にしないと`<`が\u003cへエスケープされ、リンク記法を検証できません。
func renderBlocks(t *testing.T, blocks []slack.Block) string {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(blocks); err != nil {
		t.Fatalf("marshal blocks: %v", err)
	}
	return buf.String()
}
