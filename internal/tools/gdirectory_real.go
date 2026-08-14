package tools

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/http"

	"golang.org/x/oauth2/google"
	admin "google.golang.org/api/admin/directory/v1"
	"google.golang.org/api/googleapi"
	googleoption "google.golang.org/api/option"
)

// directoryScopes はAdmin SDK Directory API連携に必要なOAuthスコープです。
// 作成専用のスコープは存在せず、これ1つで更新・停止・削除もできてしまうため、
// ツールとして公開するのは作成依頼のみに絞り、実行も承認フローの後だけにしています。
// 代理対象ユーザー(GOOGLE_IMPERSONATED_USER)にユーザー管理権限が必要です。
var directoryScopes = []string{admin.AdminDirectoryUserScope}

// tempPasswordLength は生成する一時パスワードの長さです。
// Workspaceの要件は8文字以上ですが、初回ログインまでの間の強度を確保するため長めにします。
const tempPasswordLength = 20

// tempPasswordAlphabet は一時パスワードに使う文字集合です。
// 読み間違えやすい文字(0/O、1/l/I)は口頭やチャットでの受け渡しで事故るため除いています。
const tempPasswordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// RealDirectoryClient はGoogle Workspaceサービスアカウント + ドメイン委任でAdmin SDKを呼び出す実装です。
type RealDirectoryClient struct {
	service *admin.Service
}

// NewRealDirectoryClient はサービスアカウント鍵JSONと代理対象ユーザーのメールアドレスからDirectoryClientを構築します。
func NewRealDirectoryClient(ctx context.Context, serviceAccountJSON, impersonatedUser string) (*RealDirectoryClient, error) {
	cfg, err := google.JWTConfigFromJSON([]byte(serviceAccountJSON), directoryScopes...)
	if err != nil {
		return nil, fmt.Errorf("parse service account json: %w", err)
	}
	cfg.Subject = impersonatedUser

	svc, err := admin.NewService(ctx, googleoption.WithHTTPClient(cfg.Client(ctx)))
	if err != nil {
		return nil, fmt.Errorf("create admin directory service: %w", err)
	}
	return &RealDirectoryClient{service: svc}, nil
}

func (c *RealDirectoryClient) UserExists(ctx context.Context, email string) (bool, error) {
	_, err := c.service.Users.Get(email).Context(ctx).Do()
	if err == nil {
		return true, nil
	}
	// 未登録は404で返る。それ以外(権限不足など)は判定できないのでエラーとして扱う。
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound {
		return false, nil
	}
	return false, fmt.Errorf("check user %s: %w", email, err)
}

func (c *RealDirectoryClient) CreateUser(ctx context.Context, req NewUserRequest) (CreatedUser, error) {
	password, err := generateTempPassword()
	if err != nil {
		return CreatedUser{}, fmt.Errorf("generate temporary password: %w", err)
	}

	user := &admin.User{
		PrimaryEmail: req.PrimaryEmail,
		Name: &admin.UserName{
			GivenName:  req.GivenName,
			FamilyName: req.FamilyName,
		},
		Password: password,
		// 一時パスワードのまま使われ続けないよう、初回ログインで変更させる。
		ChangePasswordAtNextLogin: true,
	}
	if req.OrgUnitPath != "" {
		user.OrgUnitPath = req.OrgUnitPath
	}

	created, err := c.service.Users.Insert(user).Context(ctx).Do()
	if err != nil {
		return CreatedUser{}, fmt.Errorf("create user %s: %w", req.PrimaryEmail, err)
	}

	name := req.DisplayName()
	if created.Name != nil && created.Name.FullName != "" {
		name = created.Name.FullName
	}
	return CreatedUser{
		PrimaryEmail: created.PrimaryEmail,
		Name:         name,
		TempPassword: password,
	}, nil
}

// generateTempPassword は暗号論的乱数で一時パスワードを生成します。
func generateTempPassword() (string, error) {
	max := big.NewInt(int64(len(tempPasswordAlphabet)))
	buf := make([]byte, tempPasswordLength)
	for i := range buf {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		buf[i] = tempPasswordAlphabet[n.Int64()]
	}
	return string(buf), nil
}
