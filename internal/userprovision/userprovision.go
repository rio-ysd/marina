// Package userprovision はGoogle Workspaceアカウント作成の承認フローを提供します。
// Claudeがツールを呼んだ時点では作成せず、承認者へYes/No確認のDMを送り、
// Yesが押されて初めてAdmin SDKで作成します。
// 代理返信フロー(internal/proxyreply)と同じ形です。
package userprovision

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/slack-go/slack"

	"github.com/yoshida-rio/marina/internal/storage"
	"github.com/yoshida-rio/marina/internal/tools"
)

// ボタンのaction_id。InteractionHandlerがこの値で振り分けます。
const (
	ActionApprove = "user_creation_approve"
	ActionReject  = "user_creation_reject"
)

// Action はSlackのボタン押下1件を表します。
type Action struct {
	ActionID        string
	Value           string
	ActorUserID     string
	ApprovalChannel string
	ApprovalTS      string
}

// SlackClient はServiceが使うSlack Web APIの操作です(テストで差し替えられるようにしています)。
type SlackClient interface {
	PostMessageContext(ctx context.Context, channelID string, options ...slack.MsgOption) (string, string, error)
	UpdateMessageContext(ctx context.Context, channelID, timestamp string, options ...slack.MsgOption) (string, string, string, error)
	OpenConversationContext(ctx context.Context, params *slack.OpenConversationParameters) (*slack.Channel, bool, bool, error)
}

// requestStore は承認待ちリクエストの保存先です。テストでDBを使わずに差し替えられるよう
// インターフェースにしています(本番は*storage.UserCreationRequestRepoがそのまま満たします)。
type requestStore interface {
	Create(ctx context.Context, req storage.UserCreationRequest) (int64, error)
	Get(ctx context.Context, id int64) (*storage.UserCreationRequest, error)
	SetApprovalMessage(ctx context.Context, id int64, channel, ts string) error
	ClaimDecision(ctx context.Context, id int64, status string, decidedAt time.Time) (bool, error)
	ReleaseDecision(ctx context.Context, id int64) error
	SetCreatedEmail(ctx context.Context, id int64, email string) error
}

// Service はアカウント作成の承認フローを処理します。
type Service struct {
	// approverUserID は承認できる唯一のSlackユーザー。この人以外のボタン押下は無視します。
	approverUserID string
	requests       requestStore
	directory      tools.DirectoryClient
	slack          SlackClient
}

// NewService はServiceを構築します。approverUserIDが空の場合はnilを返し、機能を無効にします
// (承認者が決まっていない状態でアカウント作成の経路を開けないため)。
func NewService(approverUserID string, requests *storage.UserCreationRequestRepo, directory tools.DirectoryClient, slackClient SlackClient) *Service {
	if strings.TrimSpace(approverUserID) == "" {
		return nil
	}
	return &Service{
		approverUserID: approverUserID,
		requests:       requests,
		directory:      directory,
		slack:          slackClient,
	}
}

// ApproverUserID は承認者のSlackユーザーIDを返します。
func (s *Service) ApproverUserID() string { return s.approverUserID }

// RequestUserCreation はtools.UserCreationApproverの実装です。
// 依頼をpendingで保存し、承認者へYes/Noボタン付きのDMを送ります。アカウントはまだ作りません。
func (s *Service) RequestUserCreation(ctx context.Context, req tools.NewUserRequest, requester tools.Requester) (string, error) {
	id, err := s.requests.Create(ctx, storage.UserCreationRequest{
		RequesterUser:  requester.SlackUser,
		RequestChannel: requester.SlackChannel,
		PrimaryEmail:   req.PrimaryEmail,
		GivenName:      req.GivenName,
		FamilyName:     req.FamilyName,
		OrgUnitPath:    req.OrgUnitPath,
	})
	if err != nil {
		return "", fmt.Errorf("save user creation request: %w", err)
	}

	dmChannel, err := s.openApproverDM(ctx)
	if err != nil {
		return "", fmt.Errorf("open approver dm: %w", err)
	}

	_, ts, err := s.slack.PostMessageContext(ctx, dmChannel,
		slack.MsgOptionBlocks(s.approvalBlocks(id, req, requester)...),
		slack.MsgOptionText(fmt.Sprintf("アカウント作成の承認依頼: %s", req.PrimaryEmail), false),
	)
	if err != nil {
		return "", fmt.Errorf("post approval dm: %w", err)
	}
	if err := s.requests.SetApprovalMessage(ctx, id, dmChannel, ts); err != nil {
		// 記録に失敗しても承認自体は動くのでログだけ残す。
		log.Printf("userprovision: failed to save approval message ref (id=%d): %v", id, err)
	}

	return fmt.Sprintf(
		"アカウント作成の承認依頼を送りました(依頼番号 %d)。\n"+
			"宛先: %s / 氏名: %s\n"+
			"承認者が承認するとアカウントが作成されます。まだ作成されていません。",
		id, req.PrimaryEmail, req.DisplayName()), nil
}

// HandleAction はYes/Noボタンの押下を処理します。
func (s *Service) HandleAction(ctx context.Context, a Action) error {
	// 承認できるのは承認者本人だけ。他の人が押しても何もしない。
	if a.ActorUserID != s.approverUserID {
		log.Printf("userprovision: ignoring action from non-approver %s", a.ActorUserID)
		return nil
	}

	id, err := strconv.ParseInt(a.Value, 10, 64)
	if err != nil {
		return fmt.Errorf("parse request id %q: %w", a.Value, err)
	}

	switch a.ActionID {
	case ActionApprove:
		return s.approve(ctx, id, a)
	case ActionReject:
		return s.reject(ctx, id, a)
	default:
		return nil
	}
}

func (s *Service) approve(ctx context.Context, id int64, a Action) error {
	req, err := s.requests.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("load request %d: %w", id, err)
	}

	// 連打やイベント再送で二重に作らないよう、pending→approvedを取れた場合のみ実行する。
	claimed, err := s.requests.ClaimDecision(ctx, id, storage.UserCreationStatusApproved, time.Now())
	if err != nil {
		return fmt.Errorf("claim request %d: %w", id, err)
	}
	if !claimed {
		log.Printf("userprovision: request %d is already decided (status=%s)", id, req.Status)
		return nil
	}

	created, err := s.directory.CreateUser(ctx, tools.NewUserRequest{
		PrimaryEmail: req.PrimaryEmail,
		GivenName:    req.GivenName,
		FamilyName:   req.FamilyName,
		OrgUnitPath:  req.OrgUnitPath,
	})
	if err != nil {
		// 作成に失敗したらpendingへ戻し、もう一度承認できるようにする。
		if releaseErr := s.requests.ReleaseDecision(ctx, id); releaseErr != nil {
			log.Printf("userprovision: failed to release request %d: %v", id, releaseErr)
		}
		s.updateApprovalMessage(ctx, a, fmt.Sprintf(
			":warning: アカウント作成に失敗しました。\n作成対象: %s\nエラー: %s\n(依頼は未処理に戻したのでもう一度承認できます)",
			req.PrimaryEmail, err))
		return fmt.Errorf("create user for request %d: %w", id, err)
	}

	if err := s.requests.SetCreatedEmail(ctx, id, created.PrimaryEmail); err != nil {
		log.Printf("userprovision: failed to save created email (id=%d): %v", id, err)
	}

	// 一時パスワードはDBに保存しないため、この1回のDMだけが受け渡しの機会になる。
	s.updateApprovalMessage(ctx, a, fmt.Sprintf(
		":white_check_mark: アカウントを作成しました。\n*%s* (%s)\n"+
			"一時パスワード: `%s`\n"+
			"初回ログイン時にパスワード変更が必要です。このパスワードは保存されないので、本人へ安全な経路で渡してください。",
		created.PrimaryEmail, created.Name, created.TempPassword))
	return nil
}

func (s *Service) reject(ctx context.Context, id int64, a Action) error {
	req, err := s.requests.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("load request %d: %w", id, err)
	}

	claimed, err := s.requests.ClaimDecision(ctx, id, storage.UserCreationStatusRejected, time.Now())
	if err != nil {
		return fmt.Errorf("claim request %d: %w", id, err)
	}
	if !claimed {
		log.Printf("userprovision: request %d is already decided (status=%s)", id, req.Status)
		return nil
	}

	s.updateApprovalMessage(ctx, a, fmt.Sprintf(":no_entry: 作成を取りやめました(%s)。", req.PrimaryEmail))
	return nil
}

// approvalBlocks はYes/Noボタン付きの確認DMのBlock Kitを組み立てます。
func (s *Service) approvalBlocks(id int64, req tools.NewUserRequest, requester tools.Requester) []slack.Block {
	orgUnit := req.OrgUnitPath
	if orgUnit == "" {
		orgUnit = "(ルート)"
	}
	detail := fmt.Sprintf("*メールアドレス*: %s\n*氏名*: %s\n*組織部門*: %s",
		req.PrimaryEmail, req.DisplayName(), orgUnit)

	return []slack.Block{
		mrkdwnSection(fmt.Sprintf("<@%s> さんからGoogle Workspaceアカウントの作成依頼です(依頼番号 %d)。作成しますか?",
			requester.SlackUser, id)),
		mrkdwnSection(detail),
		slack.NewActionBlock(fmt.Sprintf("user_creation_%d", id),
			buttonElement(ActionApprove, id, "Yes(作成する)", slack.StylePrimary),
			buttonElement(ActionReject, id, "No(作成しない)", slack.StyleDanger),
		),
		slack.NewContextBlock("",
			slack.NewTextBlockObject(slack.MarkdownType,
				"Yesを押すと実際にアカウントが作成されます。一時パスワードはこのスレッドに一度だけ表示されます。", false, false),
		),
	}
}

// updateApprovalMessage は確認DMを結果表示に差し替えます(ボタンを消して二度押しを防ぎます)。
func (s *Service) updateApprovalMessage(ctx context.Context, a Action, text string) {
	if a.ApprovalChannel == "" || a.ApprovalTS == "" {
		return
	}
	if _, _, _, err := s.slack.UpdateMessageContext(ctx, a.ApprovalChannel, a.ApprovalTS,
		slack.MsgOptionBlocks(mrkdwnSection(text)),
		slack.MsgOptionText(text, false),
	); err != nil {
		log.Printf("userprovision: failed to update approval message: %v", err)
	}
}

// openApproverDM は承認者とのDMチャンネルIDを取得します。
func (s *Service) openApproverDM(ctx context.Context) (string, error) {
	ch, _, _, err := s.slack.OpenConversationContext(ctx, &slack.OpenConversationParameters{
		Users: []string{s.approverUserID},
	})
	if err != nil {
		return "", err
	}
	return ch.ID, nil
}

func buttonElement(actionID string, id int64, label string, style slack.Style) *slack.ButtonBlockElement {
	btn := slack.NewButtonBlockElement(actionID, strconv.FormatInt(id, 10),
		slack.NewTextBlockObject(slack.PlainTextType, label, false, false))
	btn.Style = style
	return btn
}

func mrkdwnSection(text string) *slack.SectionBlock {
	return slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, text, false, false), nil, nil)
}
