package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
)

// fakeApprover は承認フローへ渡された内容を記録するだけの実装です。
type fakeApprover struct {
	requests   []NewUserRequest
	requesters []Requester
	err        error
}

func (f *fakeApprover) RequestUserCreation(ctx context.Context, req NewUserRequest, requester Requester) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.requests = append(f.requests, req)
	f.requesters = append(f.requesters, requester)
	return "承認依頼を送りました。まだ作成されていません。", nil
}

// fakeDirectoryClient は既存アカウントの有無と作成呼び出しを制御します。
type fakeDirectoryClient struct {
	exists    bool
	existsErr error
	created   []NewUserRequest
}

func (f *fakeDirectoryClient) UserExists(ctx context.Context, email string) (bool, error) {
	return f.exists, f.existsErr
}

func (f *fakeDirectoryClient) CreateUser(ctx context.Context, req NewUserRequest) (CreatedUser, error) {
	f.created = append(f.created, req)
	return CreatedUser{PrimaryEmail: req.PrimaryEmail}, nil
}

// callTool はツールを1つだけ持つセットから名前で探して実行します。
func callTool(t *testing.T, tools []anthropic.BetaTool, name, argsJSON string) (string, error) {
	t.Helper()
	for _, tool := range tools {
		if tool.Name() != name {
			continue
		}
		ctx := WithTaskCallCtx(context.Background(), TaskCallCtx{SlackChannel: "C-GENERAL", SlackUser: "U-REQUESTER"})
		results, err := tool.Execute(ctx, []byte(argsJSON))
		if err != nil {
			return "", err
		}
		var out strings.Builder
		for _, result := range results {
			if result.OfText != nil {
				out.WriteString(result.OfText.Text)
			}
		}
		return out.String(), nil
	}
	t.Fatalf("ツール %s が見つかりません", name)
	return "", nil
}

// 承認者がいない環境ではツールを作らせない(作成の経路自体を開けない)。
func TestNewDirectoryToolsRequiresApprover(t *testing.T) {
	if _, err := NewDirectoryTools(&fakeDirectoryClient{}, nil); err == nil {
		t.Fatal("NewDirectoryTools(approver=nil) error = nil, want error")
	}
}

func TestNewDirectoryToolsDefinesOnlyRequestTool(t *testing.T) {
	directoryTools, err := NewDirectoryTools(&fakeDirectoryClient{}, &fakeApprover{})
	if err != nil {
		t.Fatalf("NewDirectoryTools() error: %v", err)
	}

	got := make(map[string]bool, len(directoryTools))
	for _, tool := range directoryTools {
		got[tool.Name()] = true
	}
	if !got["directory_request_user_creation"] {
		t.Errorf("依頼ツールが定義されていない (定義済み: %v)", got)
	}
	// 停止・削除・更新は公開しない。作成も直接実行するツールは置かない。
	for _, name := range []string{
		"directory_create_user", "directory_delete_user", "directory_suspend_user", "directory_update_user",
	} {
		if got[name] {
			t.Errorf("ツール %s は公開しない方針", name)
		}
	}
	if len(directoryTools) != 1 {
		t.Errorf("ツール数 = %d, want 1", len(directoryTools))
	}
}

// ツールを呼んでもアカウントは作られず、承認フローに載るだけであること。
func TestRequestUserCreationToolDoesNotCreate(t *testing.T) {
	client := &fakeDirectoryClient{}
	approver := &fakeApprover{}
	directoryTools, err := NewDirectoryTools(client, approver)
	if err != nil {
		t.Fatalf("NewDirectoryTools() error: %v", err)
	}

	out, err := callTool(t, directoryTools, "directory_request_user_creation",
		`{"primary_email":"taro.yamada@example.com","family_name":"山田","given_name":"太郎","org_unit_path":"営業部"}`)
	if err != nil {
		t.Fatalf("ツール実行エラー: %v", err)
	}

	if len(client.created) != 0 {
		t.Errorf("ツール呼び出しでアカウントが作られた: %+v", client.created)
	}
	if len(approver.requests) != 1 {
		t.Fatalf("承認フローに載っていない: %+v", approver.requests)
	}
	req := approver.requests[0]
	if req.PrimaryEmail != "taro.yamada@example.com" || req.FamilyName != "山田" || req.GivenName != "太郎" {
		t.Errorf("依頼内容が違う: %+v", req)
	}
	// 組織部門のパスは先頭にスラッシュが必要(無いとAPIが受け付けない)。
	if req.OrgUnitPath != "/営業部" {
		t.Errorf("OrgUnitPath = %q, want /営業部", req.OrgUnitPath)
	}
	// 依頼者のSlackユーザーが承認DMに出せるよう渡っていること。
	if approver.requesters[0].SlackUser != "U-REQUESTER" {
		t.Errorf("依頼者が渡っていない: %+v", approver.requesters[0])
	}
	if !strings.Contains(out, "まだ作成されていません") {
		t.Errorf("Claudeへの結果が誤解を招く: %q", out)
	}
}

// 既存アカウントは承認者に確認を出す前に弾く(承認後に失敗すると作られたと誤解される)。
func TestRequestUserCreationToolRejectsExistingUser(t *testing.T) {
	approver := &fakeApprover{}
	directoryTools, err := NewDirectoryTools(&fakeDirectoryClient{exists: true}, approver)
	if err != nil {
		t.Fatalf("NewDirectoryTools() error: %v", err)
	}

	_, err = callTool(t, directoryTools, "directory_request_user_creation",
		`{"primary_email":"taro.yamada@example.com","family_name":"山田","given_name":"太郎"}`)
	if err == nil {
		t.Fatal("既存アカウントなのにエラーにならなかった")
	}
	if !strings.Contains(err.Error(), "既に存在します") {
		t.Errorf("error = %v, want it to mention the duplicate", err)
	}
	if len(approver.requests) != 0 {
		t.Errorf("重複なのに承認依頼が飛んだ: %+v", approver.requests)
	}
}

// 存在確認自体が失敗した場合も承認依頼を出さない(権限不足を見逃さない)。
func TestRequestUserCreationToolStopsOnExistsError(t *testing.T) {
	approver := &fakeApprover{}
	directoryTools, err := NewDirectoryTools(
		&fakeDirectoryClient{existsErr: errors.New("insufficient permission")}, approver)
	if err != nil {
		t.Fatalf("NewDirectoryTools() error: %v", err)
	}

	if _, err := callTool(t, directoryTools, "directory_request_user_creation",
		`{"primary_email":"taro.yamada@example.com","family_name":"山田","given_name":"太郎"}`); err == nil {
		t.Fatal("存在確認の失敗がエラーになっていない")
	}
	if len(approver.requests) != 0 {
		t.Errorf("確認できていないのに承認依頼が飛んだ: %+v", approver.requests)
	}
}

func TestToNewUserRequestValidation(t *testing.T) {
	tests := []struct {
		name    string
		in      directoryCreateUserInput
		wantErr bool
	}{
		{name: "正常", in: directoryCreateUserInput{PrimaryEmail: "a.b@example.com", FamilyName: "山田", GivenName: "太郎"}},
		{name: "前後の空白は落とす", in: directoryCreateUserInput{PrimaryEmail: " a@example.com ", FamilyName: " 山田 ", GivenName: " 太郎 "}},
		{name: "@なし", in: directoryCreateUserInput{PrimaryEmail: "invalid", FamilyName: "山田", GivenName: "太郎"}, wantErr: true},
		{name: "ドメインにドットなし", in: directoryCreateUserInput{PrimaryEmail: "a@example", FamilyName: "山田", GivenName: "太郎"}, wantErr: true},
		{name: "空白入り", in: directoryCreateUserInput{PrimaryEmail: "a b@example.com", FamilyName: "山田", GivenName: "太郎"}, wantErr: true},
		{name: "姓なし", in: directoryCreateUserInput{PrimaryEmail: "a@example.com", GivenName: "太郎"}, wantErr: true},
		{name: "名なし", in: directoryCreateUserInput{PrimaryEmail: "a@example.com", FamilyName: "山田"}, wantErr: true},
	}
	for _, tt := range tests {
		got, err := tt.in.toNewUserRequest()
		if tt.wantErr {
			if err == nil {
				t.Errorf("%s: error = nil, want error", tt.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: error = %v", tt.name, err)
			continue
		}
		if strings.HasPrefix(got.PrimaryEmail, " ") || strings.HasSuffix(got.FamilyName, " ") {
			t.Errorf("%s: 空白が落ちていない: %+v", tt.name, got)
		}
	}
}

func TestNewUserRequestDisplayName(t *testing.T) {
	if got := (NewUserRequest{FamilyName: "山田", GivenName: "太郎"}).DisplayName(); got != "山田 太郎" {
		t.Errorf("DisplayName() = %q, want 山田 太郎", got)
	}
}
