package tools

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/toolrunner"
)

// NewUserRequest はGoogle Workspaceアカウントの作成内容です。
type NewUserRequest struct {
	PrimaryEmail string `json:"primary_email"`
	GivenName    string `json:"given_name"`
	FamilyName   string `json:"family_name"`
	// OrgUnitPath は所属組織部門のパス(例: /営業部)。空ならルート。
	OrgUnitPath string `json:"org_unit_path,omitempty"`
}

// CreatedUser は作成されたアカウントです。
// TempPasswordは初回ログイン時に変更を強制される一時パスワードで、保存されません。
type CreatedUser struct {
	PrimaryEmail string `json:"primary_email"`
	Name         string `json:"name"`
	TempPassword string `json:"temp_password"`
}

// Requester はツールを呼び出したSlack上の依頼者です。
type Requester struct {
	SlackUser    string
	SlackChannel string
}

// DirectoryClient はAdmin SDK Directory APIを抽象化するインターフェースです。
// 停止・削除・更新は取り返しがつかないため、意図的に公開していません。
type DirectoryClient interface {
	// UserExists は指定メールアドレスのアカウントが既に存在するかを返します。
	UserExists(ctx context.Context, email string) (bool, error)
	// CreateUser はアカウントを作成し、一時パスワードを含む結果を返します。
	// 承認フローを通ったあとにのみ呼ばれます。
	CreateUser(ctx context.Context, req NewUserRequest) (CreatedUser, error)
}

// UserCreationApprover はアカウント作成依頼を承認フローに載せる実装です(internal/userprovision.Service)。
// Claudeがツールを呼んだ時点では作成せず、承認者がSlackのYes/Noボタンを押して初めて実行されます。
type UserCreationApprover interface {
	// RequestUserCreation は依頼を登録して承認者へ確認DMを送り、依頼者へ返す文面を返します。
	RequestUserCreation(ctx context.Context, req NewUserRequest, requester Requester) (string, error)
}

// MockDirectoryClient はGoogle認証情報が未設定の間に使うスタブ実装です。
type MockDirectoryClient struct{}

func NewMockDirectoryClient() *MockDirectoryClient { return &MockDirectoryClient{} }

func (m *MockDirectoryClient) UserExists(ctx context.Context, email string) (bool, error) {
	log.Printf("[MockDirectoryClient] UserExists(%s) called (Google認証情報が未設定のためモック応答)", email)
	return false, nil
}

func (m *MockDirectoryClient) CreateUser(ctx context.Context, req NewUserRequest) (CreatedUser, error) {
	log.Printf("[MockDirectoryClient] CreateUser(%s) called (Google認証情報が未設定のためモック応答)", req.PrimaryEmail)
	return CreatedUser{
		PrimaryEmail: req.PrimaryEmail,
		Name:         req.FamilyName + " " + req.GivenName,
		TempPassword: "mock-temporary-password",
	}, nil
}

// emailPattern は最低限の形式チェックです。実在するドメインかはAPI側が判定します。
var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s.]+\.[^@\s]+$`)

// NewDirectoryTools はアカウント作成依頼用のBetaToolセットを構築します。
// approverが必要なため、承認者が設定されていない環境ではこのツール自体を登録しません
// (登録しなければClaudeは作成を試みることすらできません)。
func NewDirectoryTools(client DirectoryClient, approver UserCreationApprover) ([]anthropic.BetaTool, error) {
	if approver == nil {
		return nil, fmt.Errorf("user creation approver is required")
	}

	requestTool, err := toolrunner.NewBetaToolFromBytes[directoryCreateUserInput](
		"directory_request_user_creation",
		"Google Workspaceアカウントの作成を依頼する。このツールはアカウントを作らず、"+
			"承認者へYes/No確認のDMを送るだけ。承認者がYesを押して初めて作成される。"+
			"ユーザーには「承認依頼を送った」ことを伝え、作成済みとは言わないこと。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"primary_email": map[string]any{"type": "string", "description": "作成するアカウントのメールアドレス(例: taro.yamada@example.com)"},
				"family_name":   map[string]any{"type": "string", "description": "姓"},
				"given_name":    map[string]any{"type": "string", "description": "名"},
				"org_unit_path": map[string]any{"type": "string", "description": "所属組織部門のパス(例: /営業部)。省略時はルート"},
			},
			"required": []string{"primary_email", "family_name", "given_name"},
		}),
		func(ctx context.Context, in directoryCreateUserInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			req, err := in.toNewUserRequest()
			if err != nil {
				return textResult(""), err
			}

			// 承認者に確認を出す前に、既存アカウントとの重複を弾いておく
			// (承認後に失敗すると、承認者は作られたと思ったままになる)。
			exists, err := client.UserExists(ctx, req.PrimaryEmail)
			if err != nil {
				return textResult(""), err
			}
			if exists {
				return textResult(""), fmt.Errorf("%s のアカウントは既に存在します", req.PrimaryEmail)
			}

			callCtx, err := taskCallCtxFrom(ctx)
			if err != nil {
				return textResult(""), err
			}
			message, err := approver.RequestUserCreation(ctx, req, Requester{
				SlackUser:    callCtx.SlackUser,
				SlackChannel: callCtx.SlackChannel,
			})
			if err != nil {
				return textResult(""), err
			}
			return textResult(message), nil
		},
	)
	if err != nil {
		return nil, err
	}

	return []anthropic.BetaTool{requestTool}, nil
}

// toNewUserRequest は入力を検証してNewUserRequestへ変換します。
func (in directoryCreateUserInput) toNewUserRequest() (NewUserRequest, error) {
	req := NewUserRequest{
		PrimaryEmail: strings.TrimSpace(in.PrimaryEmail),
		GivenName:    strings.TrimSpace(in.GivenName),
		FamilyName:   strings.TrimSpace(in.FamilyName),
		OrgUnitPath:  strings.TrimSpace(in.OrgUnitPath),
	}
	if !emailPattern.MatchString(req.PrimaryEmail) {
		return NewUserRequest{}, fmt.Errorf("メールアドレス %q の形式が不正です", in.PrimaryEmail)
	}
	if req.GivenName == "" || req.FamilyName == "" {
		return NewUserRequest{}, fmt.Errorf("姓と名の両方が必要です")
	}
	// 組織部門のパスは先頭がスラッシュでないとAPIが受け付けない。
	if req.OrgUnitPath != "" && !strings.HasPrefix(req.OrgUnitPath, "/") {
		req.OrgUnitPath = "/" + req.OrgUnitPath
	}
	return req, nil
}

// DisplayName は「姓 名」形式の表示名を返します。
func (r NewUserRequest) DisplayName() string {
	return strings.TrimSpace(r.FamilyName + " " + r.GivenName)
}

type directoryCreateUserInput struct {
	PrimaryEmail string `json:"primary_email"`
	FamilyName   string `json:"family_name"`
	GivenName    string `json:"given_name"`
	OrgUnitPath  string `json:"org_unit_path,omitempty"`
}
