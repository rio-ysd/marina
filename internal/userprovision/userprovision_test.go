package userprovision

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/slack-go/slack"

	"github.com/yoshida-rio/marina/internal/storage"
	"github.com/yoshida-rio/marina/internal/tools"
)

// fakeStore はDBを使わない承認待ちリクエストの保存先です。
type fakeStore struct {
	requests    map[int64]*storage.UserCreationRequest
	nextID      int64
	released    []int64
	createdMail map[int64]string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		requests:    map[int64]*storage.UserCreationRequest{},
		createdMail: map[int64]string{},
	}
}

func (f *fakeStore) Create(ctx context.Context, req storage.UserCreationRequest) (int64, error) {
	f.nextID++
	req.ID = f.nextID
	req.Status = storage.UserCreationStatusPending
	f.requests[req.ID] = &req
	return req.ID, nil
}

func (f *fakeStore) Get(ctx context.Context, id int64) (*storage.UserCreationRequest, error) {
	req, ok := f.requests[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return req, nil
}

func (f *fakeStore) SetApprovalMessage(ctx context.Context, id int64, channel, ts string) error {
	if req, ok := f.requests[id]; ok {
		req.ApprovalChannel, req.ApprovalTS = channel, ts
	}
	return nil
}

func (f *fakeStore) ClaimDecision(ctx context.Context, id int64, status string, decidedAt time.Time) (bool, error) {
	req, ok := f.requests[id]
	if !ok {
		return false, errors.New("not found")
	}
	if req.Status != storage.UserCreationStatusPending {
		return false, nil
	}
	req.Status = status
	return true, nil
}

func (f *fakeStore) ReleaseDecision(ctx context.Context, id int64) error {
	f.released = append(f.released, id)
	if req, ok := f.requests[id]; ok {
		req.Status = storage.UserCreationStatusPending
	}
	return nil
}

func (f *fakeStore) SetCreatedEmail(ctx context.Context, id int64, email string) error {
	f.createdMail[id] = email
	return nil
}

// fakeSlack は投稿内容を記録するだけのSlackクライアントです。
type fakeSlack struct {
	posted  []string
	updated []string
	dmUser  string
}

func (f *fakeSlack) PostMessageContext(ctx context.Context, channelID string, options ...slack.MsgOption) (string, string, error) {
	f.posted = append(f.posted, channelID)
	return channelID, "1700000000.000100", nil
}

func (f *fakeSlack) UpdateMessageContext(ctx context.Context, channelID, timestamp string, options ...slack.MsgOption) (string, string, string, error) {
	// MsgOptionから本文を取り出すのは手間なので、更新が呼ばれたことだけを記録する。
	f.updated = append(f.updated, channelID+"/"+timestamp)
	return channelID, timestamp, "", nil
}

func (f *fakeSlack) OpenConversationContext(ctx context.Context, params *slack.OpenConversationParameters) (*slack.Channel, bool, bool, error) {
	if len(params.Users) > 0 {
		f.dmUser = params.Users[0]
	}
	ch := &slack.Channel{}
	ch.ID = "D-APPROVER"
	return ch, false, false, nil
}

// fakeDirectory はアカウント作成の呼び出しを記録するクライアントです。
type fakeDirectory struct {
	created []tools.NewUserRequest
	err     error
}

func (f *fakeDirectory) UserExists(ctx context.Context, email string) (bool, error) {
	return false, nil
}

func (f *fakeDirectory) CreateUser(ctx context.Context, req tools.NewUserRequest) (tools.CreatedUser, error) {
	if f.err != nil {
		return tools.CreatedUser{}, f.err
	}
	f.created = append(f.created, req)
	return tools.CreatedUser{PrimaryEmail: req.PrimaryEmail, Name: req.DisplayName(), TempPassword: "temp-pass"}, nil
}

func newTestService(t *testing.T) (*Service, *fakeStore, *fakeSlack, *fakeDirectory) {
	t.Helper()
	store, slackClient, directory := newFakeStore(), &fakeSlack{}, &fakeDirectory{}
	return &Service{
		approverUserID: "U-APPROVER",
		requests:       store,
		directory:      directory,
		slack:          slackClient,
	}, store, slackClient, directory
}

var sampleRequest = tools.NewUserRequest{
	PrimaryEmail: "taro.yamada@example.com",
	GivenName:    "太郎",
	FamilyName:   "山田",
	OrgUnitPath:  "/営業部",
}

// 承認者が設定されていない場合は機能を丸ごと無効にする(nilを返す)。
func TestNewServiceRequiresApprover(t *testing.T) {
	for _, approver := range []string{"", "   "} {
		if svc := NewService(approver, nil, nil, nil); svc != nil {
			t.Errorf("NewService(%q) = non-nil, want nil", approver)
		}
	}
}

// ツール呼び出しの時点ではアカウントを作らず、承認DMを送るだけであること。
func TestRequestUserCreationDoesNotCreateYet(t *testing.T) {
	svc, store, slackClient, directory := newTestService(t)

	msg, err := svc.RequestUserCreation(context.Background(), sampleRequest,
		tools.Requester{SlackUser: "U-REQUESTER", SlackChannel: "C-GENERAL"})
	if err != nil {
		t.Fatalf("RequestUserCreation() error: %v", err)
	}

	if len(directory.created) != 0 {
		t.Errorf("この時点でアカウントが作られている: %+v", directory.created)
	}
	if len(store.requests) != 1 {
		t.Fatalf("依頼が保存されていない: %+v", store.requests)
	}
	req := store.requests[1]
	if req.Status != storage.UserCreationStatusPending {
		t.Errorf("status = %q, want pending", req.Status)
	}
	if req.PrimaryEmail != sampleRequest.PrimaryEmail || req.OrgUnitPath != "/営業部" {
		t.Errorf("保存内容が違う: %+v", req)
	}
	// 確認DMは承認者へ送る(依頼者ではない)。
	if slackClient.dmUser != "U-APPROVER" {
		t.Errorf("確認DMの宛先 = %q, want U-APPROVER", slackClient.dmUser)
	}
	if len(slackClient.posted) != 1 || slackClient.posted[0] != "D-APPROVER" {
		t.Errorf("確認DMが送られていない: %+v", slackClient.posted)
	}
	if req.ApprovalChannel != "D-APPROVER" || req.ApprovalTS == "" {
		t.Errorf("確認DMの参照が記録されていない: %+v", req)
	}
	// 依頼者に返す文面は「まだ作成されていない」ことが伝わる内容であること。
	if !strings.Contains(msg, "まだ作成されていません") {
		t.Errorf("返却メッセージが誤解を招く: %q", msg)
	}
}

func TestApproveCreatesUser(t *testing.T) {
	svc, store, slackClient, directory := newTestService(t)
	id, _ := store.Create(context.Background(), storage.UserCreationRequest{
		PrimaryEmail: sampleRequest.PrimaryEmail,
		GivenName:    sampleRequest.GivenName,
		FamilyName:   sampleRequest.FamilyName,
		OrgUnitPath:  sampleRequest.OrgUnitPath,
	})

	err := svc.HandleAction(context.Background(), Action{
		ActionID: ActionApprove, Value: itoa(id), ActorUserID: "U-APPROVER",
		ApprovalChannel: "D-APPROVER", ApprovalTS: "1700000000.000100",
	})
	if err != nil {
		t.Fatalf("HandleAction() error: %v", err)
	}

	if len(directory.created) != 1 {
		t.Fatalf("アカウントが作られていない: %+v", directory.created)
	}
	if directory.created[0].PrimaryEmail != sampleRequest.PrimaryEmail {
		t.Errorf("作成内容が違う: %+v", directory.created[0])
	}
	if store.requests[id].Status != storage.UserCreationStatusApproved {
		t.Errorf("status = %q, want approved", store.requests[id].Status)
	}
	if store.createdMail[id] != sampleRequest.PrimaryEmail {
		t.Errorf("作成結果が記録されていない: %q", store.createdMail[id])
	}
	// ボタンを消すために確認DMを更新する(二度押し防止)。
	if len(slackClient.updated) != 1 {
		t.Errorf("確認DMが更新されていない: %+v", slackClient.updated)
	}
}

// 承認者以外が押しても何も起きないこと。ここが破られると誰でもアカウントを作れてしまう。
func TestHandleActionIgnoresNonApprover(t *testing.T) {
	svc, store, _, directory := newTestService(t)
	id, _ := store.Create(context.Background(), storage.UserCreationRequest{PrimaryEmail: sampleRequest.PrimaryEmail})

	err := svc.HandleAction(context.Background(), Action{
		ActionID: ActionApprove, Value: itoa(id), ActorUserID: "U-SOMEONE-ELSE",
	})
	if err != nil {
		t.Fatalf("HandleAction() error: %v", err)
	}
	if len(directory.created) != 0 {
		t.Errorf("承認者以外の操作でアカウントが作られた: %+v", directory.created)
	}
	if store.requests[id].Status != storage.UserCreationStatusPending {
		t.Errorf("status = %q, want pending", store.requests[id].Status)
	}
}

// 連打やイベント再送で二重に作られないこと。
func TestApproveIsIdempotent(t *testing.T) {
	svc, store, _, directory := newTestService(t)
	id, _ := store.Create(context.Background(), storage.UserCreationRequest{PrimaryEmail: sampleRequest.PrimaryEmail})

	action := Action{ActionID: ActionApprove, Value: itoa(id), ActorUserID: "U-APPROVER",
		ApprovalChannel: "D-APPROVER", ApprovalTS: "1700000000.000100"}
	if err := svc.HandleAction(context.Background(), action); err != nil {
		t.Fatalf("1回目: %v", err)
	}
	if err := svc.HandleAction(context.Background(), action); err != nil {
		t.Fatalf("2回目: %v", err)
	}
	if len(directory.created) != 1 {
		t.Errorf("作成回数 = %d, want 1", len(directory.created))
	}
}

// 作成に失敗したらpendingへ戻し、もう一度承認できるようにすること。
func TestApproveReleasesOnFailure(t *testing.T) {
	svc, store, slackClient, directory := newTestService(t)
	directory.err = errors.New("insufficient permission")
	id, _ := store.Create(context.Background(), storage.UserCreationRequest{PrimaryEmail: sampleRequest.PrimaryEmail})

	err := svc.HandleAction(context.Background(), Action{
		ActionID: ActionApprove, Value: itoa(id), ActorUserID: "U-APPROVER",
		ApprovalChannel: "D-APPROVER", ApprovalTS: "1700000000.000100",
	})
	if err == nil {
		t.Fatal("HandleAction() error = nil, want error")
	}
	if len(store.released) != 1 {
		t.Errorf("pendingへ戻していない: %+v", store.released)
	}
	if store.requests[id].Status != storage.UserCreationStatusPending {
		t.Errorf("status = %q, want pending", store.requests[id].Status)
	}
	// 失敗も承認者に伝える。
	if len(slackClient.updated) != 1 {
		t.Errorf("失敗が通知されていない: %+v", slackClient.updated)
	}
}

func TestRejectDoesNotCreate(t *testing.T) {
	svc, store, slackClient, directory := newTestService(t)
	id, _ := store.Create(context.Background(), storage.UserCreationRequest{PrimaryEmail: sampleRequest.PrimaryEmail})

	err := svc.HandleAction(context.Background(), Action{
		ActionID: ActionReject, Value: itoa(id), ActorUserID: "U-APPROVER",
		ApprovalChannel: "D-APPROVER", ApprovalTS: "1700000000.000100",
	})
	if err != nil {
		t.Fatalf("HandleAction() error: %v", err)
	}
	if len(directory.created) != 0 {
		t.Errorf("Noを押したのに作られた: %+v", directory.created)
	}
	if store.requests[id].Status != storage.UserCreationStatusRejected {
		t.Errorf("status = %q, want rejected", store.requests[id].Status)
	}
	if len(slackClient.updated) != 1 {
		t.Errorf("確認DMが更新されていない: %+v", slackClient.updated)
	}
}

func TestHandleActionRejectsBadValue(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	if err := svc.HandleAction(context.Background(), Action{
		ActionID: ActionApprove, Value: "not-a-number", ActorUserID: "U-APPROVER",
	}); err == nil {
		t.Fatal("HandleAction() error = nil, want error")
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
