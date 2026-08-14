package tools

import (
	"context"
	"fmt"
	"log"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/toolrunner"
)

// DirectoryPerson は社内ディレクトリまたは連絡先の1人分の情報です。
type DirectoryPerson struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
	// Organization は所属(部署・役職)をまとめた文字列です。
	Organization string `json:"organization,omitempty"`
	Phone        string `json:"phone,omitempty"`
}

// 検索件数の既定値と上限。People APIのpageSizeは検索系で最大30です。
const (
	defaultPeopleLimit = 10
	maxPeopleLimit     = 30
)

func normalizePeopleLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultPeopleLimit
	case limit > maxPeopleLimit:
		return maxPeopleLimit
	default:
		return limit
	}
}

// PeopleClient はPeople API連携に必要な操作を抽象化するインターフェースです。
// 参照専用で、連絡先やプロフィールの作成・更新は行いません。
type PeopleClient interface {
	// SearchDirectory は社内ディレクトリ(Workspaceのユーザー)を名前やメールの前方一致で検索します。
	SearchDirectory(ctx context.Context, query string, limit int) ([]DirectoryPerson, error)
	// SearchContacts は代理対象ユーザー個人の連絡先を検索します。
	SearchContacts(ctx context.Context, query string, limit int) ([]DirectoryPerson, error)
}

// MockPeopleClient はGoogle認証情報が未設定の間に使うスタブ実装です。
type MockPeopleClient struct{}

func NewMockPeopleClient() *MockPeopleClient { return &MockPeopleClient{} }

func (m *MockPeopleClient) SearchDirectory(ctx context.Context, query string, limit int) ([]DirectoryPerson, error) {
	log.Printf("[MockPeopleClient] SearchDirectory(%s) called (Google認証情報が未設定のためモック応答)", query)
	return []DirectoryPerson{{
		Name: "[サンプル] 田中 みゆ", Email: "miyu.tanaka@example.com", Organization: "開発部 / エンジニア",
	}}, nil
}

func (m *MockPeopleClient) SearchContacts(ctx context.Context, query string, limit int) ([]DirectoryPerson, error) {
	log.Printf("[MockPeopleClient] SearchContacts(%s) called (Google認証情報が未設定のためモック応答)", query)
	return []DirectoryPerson{{
		Name: "[サンプル] 外部 太郎", Email: "taro@example.com",
	}}, nil
}

// NewPeopleTools はClaudeのFunction CallingからPeopleClientを操作するためのBetaToolセットを構築します。
func NewPeopleTools(client PeopleClient) ([]anthropic.BetaTool, error) {
	directoryTool, err := toolrunner.NewBetaToolFromBytes[peopleSearchInput](
		"people_search_directory",
		"社内メンバー(Google Workspaceのユーザー)を名前やメールアドレスで検索する。"+
			"「〇〇さんのメアド教えて」「〇〇さんって何部だっけ」等に使う。"+
			"名前・メールの前方一致で検索されるため、姓だけ・名だけでも引ける。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "検索文字列(名前またはメールアドレスの一部)"},
				"limit": map[string]any{"type": "integer", "description": "最大件数(既定10、最大30)"},
			},
			"required": []string{"query"},
		}),
		func(ctx context.Context, in peopleSearchInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			if strings.TrimSpace(in.Query) == "" {
				return textResult(""), fmt.Errorf("検索文字列が空です")
			}
			people, err := client.SearchDirectory(ctx, in.Query, in.Limit)
			if err != nil {
				return textResult(""), err
			}
			return textResult(formatDirectoryPeople("社内メンバー", in.Query, people)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	contactsTool, err := toolrunner.NewBetaToolFromBytes[peopleSearchInput](
		"people_search_contacts",
		"個人の連絡先(Googleコンタクト)を検索する。社外の相手を探すときに使う。"+
			"社内メンバーを探す場合はpeople_search_directoryを使うこと。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "検索文字列(名前・メールアドレス・会社名の一部)"},
				"limit": map[string]any{"type": "integer", "description": "最大件数(既定10、最大30)"},
			},
			"required": []string{"query"},
		}),
		func(ctx context.Context, in peopleSearchInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			if strings.TrimSpace(in.Query) == "" {
				return textResult(""), fmt.Errorf("検索文字列が空です")
			}
			people, err := client.SearchContacts(ctx, in.Query, in.Limit)
			if err != nil {
				return textResult(""), err
			}
			return textResult(formatDirectoryPeople("連絡先", in.Query, people)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	return []anthropic.BetaTool{directoryTool, contactsTool}, nil
}

// formatDirectoryPeople は検索結果をClaudeが読みやすいテキストにまとめます。
func formatDirectoryPeople(kind, query string, people []DirectoryPerson) string {
	if len(people) == 0 {
		return fmt.Sprintf("%s「%s」の検索結果: 0件(該当する人は見つかりませんでした)", kind, query)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s「%s」の検索結果: %d件\n", kind, query, len(people))
	for _, p := range people {
		fmt.Fprintf(&b, "- %s", p.Name)
		if p.Email != "" {
			fmt.Fprintf(&b, " <%s>", p.Email)
		}
		if p.Organization != "" {
			fmt.Fprintf(&b, " (%s)", p.Organization)
		}
		if p.Phone != "" {
			fmt.Fprintf(&b, " TEL: %s", p.Phone)
		}
		b.WriteString("\n")
	}
	return b.String()
}

type peopleSearchInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}
