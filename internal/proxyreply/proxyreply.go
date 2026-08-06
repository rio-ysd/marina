// Package proxyreply は「対象ユーザー宛のメンションに対してAIが返信案を作成し、
// 本人にDMで確認(Yes/No)を取ってから、本人のアカウントとして返信する」フローを提供します。
package proxyreply

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/slack-go/slack"

	"github.com/yoshida-rio/marina/internal/storage"
)

// 確認DMのボタンのaction_id。internal/slackのインタラクションハンドラから渡されます。
const (
	ActionApprove = "proxy_reply_approve"
	ActionReject  = "proxy_reply_reject"
)

// Slackのsectionブロックのtextは3000文字までなので、表示用に切り詰めます。
const maxDisplayTextLen = 2500

// threadContextLimit は返信案作成時に参照するスレッド発言の最大件数です。
const threadContextLimit = 20

// Drafter は返信案を生成するインターフェースです。internal/agent.Agentが実装します。
type Drafter interface {
	DraftReply(ctx context.Context, targetUser, requesterUser, requestText string, threadContext []string, isDM bool) (string, error)
}

// Config は代理返信フローの設定です。
type Config struct {
	// TargetUserID は代理返信の対象ユーザー(本人)のSlackユーザーIDです。
	TargetUserID string
	// AllowedChannels はチャンネルでのメンションを受け付けるチャンネルIDの許可リストです。
	// 空の場合はBotが参加している全チャンネルを対象にします。DMには影響しません。
	AllowedChannels []string
	// IncludeDM は本人へのDMも代理返信の対象にするかどうかです。
	IncludeDM bool
}

// Service は代理返信フローを実行します。
type Service struct {
	targetUserID    string
	allowedChannels map[string]struct{}
	includeDM       bool
	drafter         Drafter
	drafts          *storage.ReplyDraftRepo
	// botClient は確認DMの送信・更新に使うBotトークン(xoxb-)のクライアントです。
	botClient *slack.Client
	// userClient は対象ユーザー本人として投稿するUser OAuthトークン(xoxp-)のクライアントです。
	// 本人のDMはBotから見えないため、DMの履歴取得もこちらを使います。
	userClient *slack.Client
	userNames  sync.Map // ユーザーID -> 表示名
}

// IncomingMessage は代理返信の対象となる受信メッセージです。
// チャンネルでの本人宛メンション(IsDM=false)と、本人へのDM(IsDM=true)の両方を表します。
type IncomingMessage struct {
	Channel   string
	User      string
	Text      string
	MessageTS string
	ThreadTS  string
	IsDM      bool
}

// Action は確認DMのボタン押下を表します。
type Action struct {
	ActionID        string
	Value           string
	ActorUserID     string
	ApprovalChannel string
	ApprovalTS      string
}

// NewService はServiceを構築します。
func NewService(cfg Config, drafter Drafter, drafts *storage.ReplyDraftRepo, botClient, userClient *slack.Client) *Service {
	allowed := make(map[string]struct{}, len(cfg.AllowedChannels))
	for _, c := range cfg.AllowedChannels {
		if c = strings.TrimSpace(c); c != "" {
			allowed[c] = struct{}{}
		}
	}
	return &Service{
		targetUserID:    cfg.TargetUserID,
		allowedChannels: allowed,
		includeDM:       cfg.IncludeDM,
		drafter:         drafter,
		drafts:          drafts,
		botClient:       botClient,
		userClient:      userClient,
	}
}

// TargetUserID は代理返信の対象ユーザーIDを返します。
func (s *Service) TargetUserID() string {
	return s.targetUserID
}

// Handles はこのメッセージを代理返信フローで処理すべきかを判定します。
// DMは宛先が本人自身なのでメンション不要、チャンネルでは本人へのメンションと許可リストを条件にします。
// いずれも発言者が本人自身の場合は対象外です(自分宛のメモ等で起動しないため)。
func (s *Service) Handles(msg IncomingMessage) bool {
	if s == nil || s.targetUserID == "" || s.userClient == nil {
		return false
	}
	if msg.User == s.targetUserID {
		return false
	}
	if msg.IsDM {
		return s.includeDM
	}
	if !strings.Contains(msg.Text, "<@"+s.targetUserID+">") {
		return false
	}
	if len(s.allowedChannels) > 0 {
		if _, ok := s.allowedChannels[msg.Channel]; !ok {
			return false
		}
	}
	return true
}

// HandleMessage は返信案を作成し、対象ユーザーへ確認DM(Yes/Noボタン付き)を送信します。
func (s *Service) HandleMessage(ctx context.Context, msg IncomingMessage) error {
	threadTS := msg.ThreadTS
	if threadTS == "" {
		threadTS = msg.MessageTS
	}

	targetName := s.userName(ctx, s.targetUserID)
	requesterName := s.userName(ctx, msg.User)
	requestText := s.resolveMentions(ctx, msg.Text)
	threadContext := s.threadContext(ctx, msg)

	draft, err := s.drafter.DraftReply(ctx, targetName, requesterName, requestText, threadContext, msg.IsDM)
	if err != nil {
		return fmt.Errorf("draft reply: %w", err)
	}

	draftID, err := s.drafts.Create(ctx, storage.ReplyDraft{
		TargetUser:      s.targetUserID,
		SourceChannel:   msg.Channel,
		SourceThreadTS:  threadTS,
		SourceMessageTS: msg.MessageTS,
		RequesterUser:   msg.User,
		RequestText:     requestText,
		DraftText:       draft,
	})
	if err != nil {
		return fmt.Errorf("save draft: %w", err)
	}

	dmChannel, err := s.openDM(ctx)
	if err != nil {
		return fmt.Errorf("open dm: %w", err)
	}

	fallback := fmt.Sprintf("%s さんへの返信案の確認です", requesterName)
	_, ts, err := s.botClient.PostMessageContext(ctx, dmChannel,
		slack.MsgOptionText(fallback, false),
		slack.MsgOptionBlocks(s.approvalBlocks(draftID, msg.User, msg.Channel, requestText, draft, "")...),
	)
	if err != nil {
		return fmt.Errorf("post approval dm: %w", err)
	}

	if err := s.drafts.SetApprovalMessage(ctx, draftID, dmChannel, ts); err != nil {
		return fmt.Errorf("save approval message: %w", err)
	}
	return nil
}

// HandleAction は確認DMのYes/Noボタン押下を処理します。
func (s *Service) HandleAction(ctx context.Context, a Action) error {
	draftID, err := strconv.ParseInt(a.Value, 10, 64)
	if err != nil {
		return fmt.Errorf("parse draft id %q: %w", a.Value, err)
	}
	if a.ActorUserID != s.targetUserID {
		return fmt.Errorf("user %s is not allowed to decide draft %d", a.ActorUserID, draftID)
	}

	switch a.ActionID {
	case ActionApprove:
		return s.approve(ctx, draftID, a)
	case ActionReject:
		return s.reject(ctx, draftID, a)
	default:
		return fmt.Errorf("unknown action id %q", a.ActionID)
	}
}

func (s *Service) approve(ctx context.Context, draftID int64, a Action) error {
	claimed, err := s.drafts.ClaimDecision(ctx, draftID, storage.ReplyDraftStatusApproved, time.Now())
	if err != nil {
		return fmt.Errorf("claim draft %d: %w", draftID, err)
	}
	if !claimed {
		log.Printf("proxyreply: draft %d is already decided, ignoring approve", draftID)
		return nil
	}

	draft, err := s.drafts.Get(ctx, draftID)
	if err != nil {
		return fmt.Errorf("get draft %d: %w", draftID, err)
	}

	_, ts, err := s.userClient.PostMessageContext(ctx, draft.SourceChannel,
		slack.MsgOptionText(draft.DraftText, false),
		slack.MsgOptionTS(draft.SourceThreadTS),
	)
	if err != nil {
		// 送信できなかった場合はpendingへ戻し、確認DMもボタン付きのまま残して再試行できるようにする。
		if relErr := s.drafts.ReleaseDecision(ctx, draftID); relErr != nil {
			log.Printf("proxyreply: release draft %d failed: %v", draftID, relErr)
		}
		notice := fmt.Sprintf(":warning: 送信に失敗しました(%v)。もう一度Yesを押すと再送信します。", err)
		s.updateApprovalMessage(ctx, a, notice,
			s.approvalBlocks(draftID, draft.RequesterUser, draft.SourceChannel, draft.RequestText, draft.DraftText, notice))
		return fmt.Errorf("post as user: %w", err)
	}

	if err := s.drafts.SetSentTS(ctx, draftID, ts); err != nil {
		log.Printf("proxyreply: save sent ts for draft %d failed: %v", draftID, err)
	}

	status := ":white_check_mark: この内容で送信しました。"
	if link := s.permalink(ctx, draft.SourceChannel, ts); link != "" {
		status = fmt.Sprintf(":white_check_mark: この内容で送信しました。<%s|送信したメッセージを開く>", link)
	}
	s.updateApprovalMessage(ctx, a, status, resultBlocks(draft.DraftText, status))
	return nil
}

func (s *Service) reject(ctx context.Context, draftID int64, a Action) error {
	claimed, err := s.drafts.ClaimDecision(ctx, draftID, storage.ReplyDraftStatusRejected, time.Now())
	if err != nil {
		return fmt.Errorf("claim draft %d: %w", draftID, err)
	}
	if !claimed {
		log.Printf("proxyreply: draft %d is already decided, ignoring reject", draftID)
		return nil
	}

	draft, err := s.drafts.Get(ctx, draftID)
	if err != nil {
		return fmt.Errorf("get draft %d: %w", draftID, err)
	}

	status := ":no_entry_sign: 送信を取りやめました。返信が必要な場合はご自身で対応してください。"
	s.updateApprovalMessage(ctx, a, status, resultBlocks(draft.DraftText, status))
	return nil
}

// approvalBlocks はYes/Noボタン付きの確認DMのBlock Kitを組み立てます。
// requestTextはメンション解決済みの元メッセージ、noticeは空でなければ先頭に表示する注意書きです。
func (s *Service) approvalBlocks(draftID int64, requesterUser, channel, requestText, draft, notice string) []slack.Block {
	blocks := make([]slack.Block, 0, 6)
	if notice != "" {
		blocks = append(blocks, mrkdwnSection(notice))
	}
	header := fmt.Sprintf("<@%s> さんから <#%s> でメンションがありました。以下の内容で返信しますか?", requesterUser, channel)
	if isDMChannel(channel) {
		header = fmt.Sprintf("<@%s> さんからDMが届きました。以下の内容で返信しますか?", requesterUser)
	}
	return append(blocks,
		mrkdwnSection(header),
		mrkdwnSection("*元のメッセージ*\n"+blockquote(requestText)),
		mrkdwnSection("*返信案*\n"+truncate(draft, maxDisplayTextLen)),
		slack.NewActionBlock(fmt.Sprintf("proxy_reply_%d", draftID),
			buttonElement(ActionApprove, draftID, "Yes(この内容で送信)", slack.StylePrimary),
			buttonElement(ActionReject, draftID, "No(送信しない)", slack.StyleDanger),
		),
		slack.NewContextBlock("",
			slack.NewTextBlockObject(slack.MarkdownType,
				fmt.Sprintf("Yesを押すと <@%s> 本人の発言として投稿します。", s.targetUserID), false, false),
		),
	)
}

// resultBlocks は決着後(送信済み/取りやめ)のボタンなし表示を組み立てます。
func resultBlocks(draft, status string) []slack.Block {
	return []slack.Block{
		mrkdwnSection("*返信案*\n" + truncate(draft, maxDisplayTextLen)),
		mrkdwnSection(status),
	}
}

// updateApprovalMessage は確認DMを指定のブロックに差し替えます。textは通知用のフォールバックです。
func (s *Service) updateApprovalMessage(ctx context.Context, a Action, text string, blocks []slack.Block) {
	if a.ApprovalChannel == "" || a.ApprovalTS == "" {
		return
	}
	if _, _, _, err := s.botClient.UpdateMessageContext(ctx, a.ApprovalChannel, a.ApprovalTS,
		slack.MsgOptionText(text, false),
		slack.MsgOptionBlocks(blocks...),
	); err != nil {
		log.Printf("proxyreply: update approval message failed: %v", err)
	}
}

func (s *Service) openDM(ctx context.Context) (string, error) {
	channel, _, _, err := s.botClient.OpenConversationContext(ctx, &slack.OpenConversationParameters{
		Users: []string{s.targetUserID},
	})
	if err != nil {
		return "", err
	}
	return channel.ID, nil
}

// readClient は会話の読み取りに使うクライアントを返します。
// 本人のDMはBotから参照できないため、DMのときは本人のUser OAuthトークンを使います。
func (s *Service) readClient(isDM bool) *slack.Client {
	if isDM {
		return s.userClient
	}
	return s.botClient
}

// threadContext はスレッド内の発言を古い順に「表示名: 本文」形式で返します。
// 取得に失敗した場合やスレッド外の発言の場合は空を返し、文脈なしで下書きを作成します。
func (s *Service) threadContext(ctx context.Context, msg IncomingMessage) []string {
	if msg.ThreadTS == "" {
		return nil
	}
	msgs, _, _, err := s.readClient(msg.IsDM).GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: msg.Channel,
		Timestamp: msg.ThreadTS,
		Limit:     threadContextLimit,
	})
	if err != nil {
		log.Printf("proxyreply: fetch thread context failed: %v", err)
		return nil
	}

	lines := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.Timestamp == msg.MessageTS {
			continue
		}
		text := strings.TrimSpace(s.resolveMentions(ctx, m.Text))
		if text == "" {
			continue
		}
		name := m.Username
		if m.User != "" {
			name = s.userName(ctx, m.User)
		}
		if name == "" {
			name = "unknown"
		}
		lines = append(lines, fmt.Sprintf("%s: %s", name, text))
	}
	return lines
}

func (s *Service) permalink(ctx context.Context, channel, ts string) string {
	link, err := s.readClient(isDMChannel(channel)).GetPermalinkContext(ctx, &slack.PermalinkParameters{Channel: channel, Ts: ts})
	if err != nil {
		log.Printf("proxyreply: get permalink failed: %v", err)
		return ""
	}
	return link
}

var mentionPattern = regexp.MustCompile(`<@([UW][A-Z0-9]+)(\|[^>]*)?>`)

// resolveMentions は `<@U123>` 形式のメンションを表示名(`@名前`)に置き換えます。
// Claudeに渡す文面を読みやすくするためのもので、下書き本文には影響しません。
func (s *Service) resolveMentions(ctx context.Context, text string) string {
	return mentionPattern.ReplaceAllStringFunc(text, func(match string) string {
		groups := mentionPattern.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		return "@" + s.userName(ctx, groups[1])
	})
}

// userName はユーザーの表示名を返します。取得できない場合はユーザーIDをそのまま返します。
func (s *Service) userName(ctx context.Context, userID string) string {
	if userID == "" {
		return ""
	}
	if cached, ok := s.userNames.Load(userID); ok {
		return cached.(string)
	}
	name := userID
	if user, err := s.botClient.GetUserInfoContext(ctx, userID); err == nil {
		switch {
		case user.Profile.DisplayName != "":
			name = user.Profile.DisplayName
		case user.RealName != "":
			name = user.RealName
		case user.Name != "":
			name = user.Name
		}
	} else {
		log.Printf("proxyreply: get user info %s failed: %v", userID, err)
	}
	s.userNames.Store(userID, name)
	return name
}

func buttonElement(actionID string, draftID int64, label string, style slack.Style) *slack.ButtonBlockElement {
	btn := slack.NewButtonBlockElement(actionID, strconv.FormatInt(draftID, 10),
		slack.NewTextBlockObject(slack.PlainTextType, label, false, false))
	btn.Style = style
	return btn
}

// isDMChannel はチャンネルIDが1対1のDM(im)かを判定します。
// SlackのDMチャンネルIDは必ず`D`で始まるため、DBに種別を持たなくても判別できます。
func isDMChannel(channelID string) bool {
	return strings.HasPrefix(channelID, "D")
}

func mrkdwnSection(text string) *slack.SectionBlock {
	return slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, truncate(text, maxDisplayTextLen), false, false), nil, nil)
}

func blockquote(text string) string {
	lines := strings.Split(truncate(text, maxDisplayTextLen), "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…(以下省略)"
}
