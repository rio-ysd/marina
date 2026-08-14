package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	googleoption "google.golang.org/api/option"
	"google.golang.org/api/people/v1"
)

// newTestPeopleClient はテスト用HTTPサーバーを向いた実クライアントを構築します。
func newTestPeopleClient(t *testing.T, handler http.HandlerFunc) *RealPeopleClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	svc, err := people.NewService(context.Background(),
		googleoption.WithHTTPClient(srv.Client()),
		googleoption.WithEndpoint(srv.URL+"/"),
	)
	if err != nil {
		t.Fatalf("people.NewService() error: %v", err)
	}
	return &RealPeopleClient{service: svc}
}

func TestSearchDirectorySendsQueryAndSources(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	client := newTestPeopleClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"people":[
			{"names":[{"displayName":"田中 みゆ"}],
			 "emailAddresses":[{"value":"miyu.tanaka@example.com"}],
			 "organizations":[{"name":"Devenue","department":"開発部","title":"エンジニア"}],
			 "phoneNumbers":[{"value":"090-0000-0000"}]}
		]}`))
	})

	people, err := client.SearchDirectory(context.Background(), "田中", 5)
	if err != nil {
		t.Fatalf("SearchDirectory() error: %v", err)
	}

	if !strings.Contains(gotPath, "people:searchDirectoryPeople") {
		t.Errorf("path = %q, want the searchDirectoryPeople endpoint", gotPath)
	}
	if got := gotQuery.Get("query"); got != "田中" {
		t.Errorf("query = %q, want 田中", got)
	}
	// readMaskを指定しないとフィールドが何も返らない。
	if got := gotQuery.Get("readMask"); !strings.Contains(got, "emailAddresses") {
		t.Errorf("readMask = %q, want it to include emailAddresses", got)
	}
	if got := gotQuery["sources"]; len(got) == 0 {
		t.Error("sources が指定されていない(社内プロフィールが検索対象にならない)")
	}
	if got := gotQuery.Get("pageSize"); got != "5" {
		t.Errorf("pageSize = %q, want 5", got)
	}

	if len(people) != 1 {
		t.Fatalf("people len = %d, want 1", len(people))
	}
	p := people[0]
	if p.Name != "田中 みゆ" || p.Email != "miyu.tanaka@example.com" || p.Phone != "090-0000-0000" {
		t.Errorf("parsed person mismatch: %+v", p)
	}
	// 会社/部署/役職はあるものだけを繋ぐ。
	if p.Organization != "Devenue / 開発部 / エンジニア" {
		t.Errorf("Organization = %q", p.Organization)
	}
}

func TestSearchDirectoryDefaultsPageSize(t *testing.T) {
	var gotQuery url.Values
	client := newTestPeopleClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"people":[]}`))
	})

	if _, err := client.SearchDirectory(context.Background(), "田中", 0); err != nil {
		t.Fatalf("SearchDirectory() error: %v", err)
	}
	if got := gotQuery.Get("pageSize"); got != "10" {
		t.Errorf("pageSize = %q, want 10 (limit未指定時の既定値)", got)
	}

	// APIの上限は30なので、それを超える指定は丸める。
	if _, err := client.SearchDirectory(context.Background(), "田中", 100); err != nil {
		t.Fatalf("SearchDirectory() error: %v", err)
	}
	if got := gotQuery.Get("pageSize"); got != "30" {
		t.Errorf("pageSize = %q, want 30 (上限に丸める)", got)
	}
}

// 連絡先検索はレスポンス構造が results[].person で異なる。
func TestSearchContactsParsesResults(t *testing.T) {
	var gotPath string
	client := newTestPeopleClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"results":[
			{"person":{"names":[{"displayName":"外部 太郎"}],"emailAddresses":[{"value":"taro@example.com"}]}},
			{}
		]}`))
	})

	got, err := client.SearchContacts(context.Background(), "太郎", 5)
	if err != nil {
		t.Fatalf("SearchContacts() error: %v", err)
	}
	if !strings.Contains(gotPath, "people:searchContacts") {
		t.Errorf("path = %q, want the searchContacts endpoint", gotPath)
	}
	// personがnilのresultは飛ばす(落ちないこと)。
	if len(got) != 1 {
		t.Fatalf("results len = %d, want 1", len(got))
	}
	if got[0].Name != "外部 太郎" || got[0].Email != "taro@example.com" {
		t.Errorf("parsed contact mismatch: %+v", got[0])
	}
}

// フィールドが1つも無いPersonでも落ちないこと。
func TestToDirectoryPersonHandlesEmptyPerson(t *testing.T) {
	if got := toDirectoryPerson(&people.Person{}); got.Name != "" || got.Email != "" {
		t.Errorf("toDirectoryPerson(empty) = %+v, want zero value", got)
	}
	// 部署だけ入っているケース。
	got := toDirectoryPerson(&people.Person{
		Organizations: []*people.Organization{{Department: "総務部"}},
	})
	if got.Organization != "総務部" {
		t.Errorf("Organization = %q, want 総務部", got.Organization)
	}
}

func TestNormalizePeopleLimit(t *testing.T) {
	for in, want := range map[int]int{0: 10, -1: 10, 5: 5, 30: 30, 100: 30} {
		if got := normalizePeopleLimit(in); got != want {
			t.Errorf("normalizePeopleLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestFormatDirectoryPeople(t *testing.T) {
	got := formatDirectoryPeople("社内メンバー", "田中", []DirectoryPerson{
		{Name: "田中 みゆ", Email: "miyu.tanaka@example.com", Organization: "開発部"},
	})
	if !strings.Contains(got, "社内メンバー「田中」の検索結果: 1件") {
		t.Errorf("見出しがおかしい:\n%s", got)
	}
	if !strings.Contains(got, "田中 みゆ <miyu.tanaka@example.com> (開発部)") {
		t.Errorf("行の整形がおかしい:\n%s", got)
	}

	// 0件は「見つからなかった」と伝わること(Claudeが推測で答えないように)。
	if empty := formatDirectoryPeople("社内メンバー", "存在しない人", nil); !strings.Contains(empty, "0件") {
		t.Errorf("0件が伝わらない: %s", empty)
	}
}

func TestNewPeopleToolsDefinesExpectedTools(t *testing.T) {
	peopleTools, err := NewPeopleTools(NewMockPeopleClient())
	if err != nil {
		t.Fatalf("NewPeopleTools() error: %v", err)
	}

	got := make(map[string]bool, len(peopleTools))
	for _, tool := range peopleTools {
		got[tool.Name()] = true
	}
	for _, name := range []string{"people_search_directory", "people_search_contacts"} {
		if !got[name] {
			t.Errorf("ツール %s が定義されていない (定義済み: %v)", name, got)
		}
	}
	if len(peopleTools) != 2 {
		t.Errorf("ツール数 = %d, want 2", len(peopleTools))
	}
}
