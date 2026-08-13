package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/yoshida-rio/marina/internal/storage"
)

// newTestInvoiceClient はテスト用HTTPサーバーを向いた実クライアントを構築します。
// 期限切れでないトークンを直接持たせ、DBアクセス(accessToken)を経由しないようにします。
func newTestInvoiceClient(t *testing.T, handler http.HandlerFunc) (*RealMFInvoiceClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := &RealMFInvoiceClient{
		oauthConfig: &oauth2.Config{},
		tokenRepo:   &fakeTokenStore{},
		httpClient:  srv.Client(),
		baseURL:     srv.URL,
	}
	return client, srv
}

// fakeTokenStore は有効期限内のトークンを返すスタブです。
// 期限内なのでoauth2.TokenSourceはリフレッシュを試みず、ネットワークアクセスも発生しません。
type fakeTokenStore struct{}

func (f *fakeTokenStore) GetByProviderAccount(ctx context.Context, provider, account string) (*storage.OAuthToken, error) {
	expiresAt := time.Now().Add(time.Hour)
	return &storage.OAuthToken{
		Provider: provider, Account: account,
		AccessToken: "test-token", RefreshToken: "test-refresh", ExpiresAt: &expiresAt,
	}, nil
}

func (f *fakeTokenStore) Upsert(ctx context.Context, provider, account, accessToken, refreshToken string, expiresAt *time.Time) error {
	return nil
}

const billingsResponse = `{
  "data": [
    {"id":"abc","billing_number":"567","billing_date":"2026/08/03","partner_name":"牧野電設工業株式会社",
     "title":"ブレーカー交換","total_price":"49500.0","payment_status":"未設定",
     "pdf_url":"https://invoice.moneyforward.com/api/v3/billings/abc.pdf"}
  ],
  "pagination": {"total_count": 5, "total_pages": 5, "per_page": 1, "current_page": 1}
}`

func TestListInvoicesSendsRangeKeyAndParsesTotalCount(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	client, _ := newTestInvoiceClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(billingsResponse))
	})

	list, err := client.ListInvoices(context.Background(), "", "2026-08-01", "2026-08-31", 20)
	if err != nil {
		t.Fatalf("ListInvoices() error: %v", err)
	}

	if gotPath != "/billings" {
		t.Errorf("path = %q, want /billings", gotPath)
	}
	// from/toはrange_keyとセットでないとAPIが422を返すため、必ず一緒に送る。
	if got := gotQuery.Get("range_key"); got != "billing_date" {
		t.Errorf("range_key = %q, want billing_date", got)
	}
	if got := gotQuery.Get("from"); got != "2026-08-01" {
		t.Errorf("from = %q", got)
	}
	if got := gotQuery.Get("to"); got != "2026-08-31" {
		t.Errorf("to = %q", got)
	}

	if list.TotalCount != 5 {
		t.Errorf("TotalCount = %d, want 5 (総件数はpaginationから取る)", list.TotalCount)
	}
	if len(list.Documents) != 1 {
		t.Fatalf("Documents len = %d, want 1", len(list.Documents))
	}
	d := list.Documents[0]
	if d.Number != 567 || d.Date != "2026/08/03" || d.PartnerName != "牧野電設工業株式会社" ||
		d.TotalPrice != 49500 || d.Status != "未設定" {
		t.Errorf("parsed document mismatch: %+v", d)
	}
}

func TestListEstimatesUsesQuoteDateRangeKey(t *testing.T) {
	var gotQuery url.Values
	client, _ := newTestInvoiceClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"data":[{"id":"q1","quote_number":"229","quote_date":"2026/08/03","order_status":"default"}],"pagination":{"total_count":26}}`))
	})

	list, err := client.ListEstimates(context.Background(), "", "2026-08-01", "2026-08-31", 0)
	if err != nil {
		t.Fatalf("ListEstimates() error: %v", err)
	}
	if got := gotQuery.Get("range_key"); got != "quote_date" {
		t.Errorf("range_key = %q, want quote_date", got)
	}
	if got := gotQuery.Get("per_page"); got != "20" {
		t.Errorf("per_page = %q, want 20 (limit未指定時の既定値)", got)
	}
	if list.TotalCount != 26 || list.Documents[0].Number != 229 || list.Documents[0].Date != "2026/08/03" {
		t.Errorf("unexpected result: %+v", list)
	}
}

func TestListInvoicesDefaultsToCurrentMonth(t *testing.T) {
	var gotQuery url.Values
	client, _ := newTestInvoiceClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"data":[],"pagination":{"total_count":0}}`))
	})

	if _, err := client.ListInvoices(context.Background(), "", "", "", 0); err != nil {
		t.Fatalf("ListInvoices() error: %v", err)
	}

	now := time.Now().In(jst)
	wantFrom := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, jst).Format(mfDateLayout)
	wantTo := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, jst).AddDate(0, 1, -1).Format(mfDateLayout)
	if got := gotQuery.Get("from"); got != wantFrom {
		t.Errorf("from = %q, want %q (今月の初日)", got, wantFrom)
	}
	if got := gotQuery.Get("to"); got != wantTo {
		t.Errorf("to = %q, want %q (今月の末日)", got, wantTo)
	}
}

// 実APIは数値を文字列で返すことも数値で返すこともあるため、両方受けられること。
func TestListInvoicesAcceptsNumericAndStringNumbers(t *testing.T) {
	client, _ := newTestInvoiceClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"a","billing_number":"567","total_price":"49500.0"},
			{"id":"b","billing_number":566,"total_price":110000.0},
			{"id":"c","billing_number":"","total_price":null}
		],"pagination":{"total_count":3}}`))
	})

	list, err := client.ListInvoices(context.Background(), "", "2026-08-01", "2026-08-31", 20)
	if err != nil {
		t.Fatalf("ListInvoices() error: %v", err)
	}
	want := []struct {
		number int
		price  float64
	}{{567, 49500}, {566, 110000}, {0, 0}}
	for i, w := range want {
		got := list.Documents[i]
		if got.Number != w.number || got.TotalPrice != w.price {
			t.Errorf("doc[%d] = number %d / price %.0f, want %d / %.0f", i, got.Number, got.TotalPrice, w.number, w.price)
		}
	}
}

// 支払期限(due_date)で絞れること。日付基準は結果にも残す。
func TestListInvoicesByDueDate(t *testing.T) {
	var gotQuery url.Values
	client, _ := newTestInvoiceClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"data":[],"pagination":{"total_count":12}}`))
	})

	list, err := client.ListInvoices(context.Background(), "due_date", "2026-08-01", "2026-09-30", 20)
	if err != nil {
		t.Fatalf("ListInvoices() error: %v", err)
	}
	if got := gotQuery.Get("range_key"); got != "due_date" {
		t.Errorf("range_key = %q, want due_date", got)
	}
	if list.DateType != "due_date" {
		t.Errorf("DateType = %q, want due_date", list.DateType)
	}
	if got := formatDocumentList("請求書", list); !strings.Contains(got, "請求書(支払期限)") {
		t.Errorf("結果に日付基準が出ていない: %s", got)
	}
}

// 未対応の値をそのまま渡すとAPIが422を返すため、既定値に落とす。
func TestListInvoicesRejectsUnsupportedDateType(t *testing.T) {
	var gotQuery url.Values
	client, _ := newTestInvoiceClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"data":[],"pagination":{"total_count":0}}`))
	})

	if _, err := client.ListInvoices(context.Background(), "bogus_key", "2026-08-01", "2026-08-31", 20); err != nil {
		t.Fatalf("ListInvoices() error: %v", err)
	}
	if got := gotQuery.Get("range_key"); got != "billing_date" {
		t.Errorf("range_key = %q, want billing_date (未対応値は既定に落とす)", got)
	}
}

func TestListEstimatesByExpiredDate(t *testing.T) {
	var gotQuery url.Values
	client, _ := newTestInvoiceClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"data":[],"pagination":{"total_count":2}}`))
	})

	if _, err := client.ListEstimates(context.Background(), "expired_date", "2026-08-01", "2026-09-30", 20); err != nil {
		t.Fatalf("ListEstimates() error: %v", err)
	}
	if got := gotQuery.Get("range_key"); got != "expired_date" {
		t.Errorf("range_key = %q, want expired_date", got)
	}
	// 見積書に請求書用の値を渡した場合も既定へ落とす。
	if _, err := client.ListEstimates(context.Background(), "due_date", "", "", 0); err != nil {
		t.Fatalf("ListEstimates() error: %v", err)
	}
	if got := gotQuery.Get("range_key"); got != "quote_date" {
		t.Errorf("range_key = %q, want quote_date", got)
	}
}

func TestNormalizeLimit(t *testing.T) {
	for in, want := range map[int]int{0: 20, -5: 20, 10: 10, 100: 100, 500: 100} {
		if got := normalizeLimit(in); got != want {
			t.Errorf("normalizeLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestFormatDocumentListShowsTruncation(t *testing.T) {
	list := DocumentList{
		From: "2026-08-01", To: "2026-08-31", TotalCount: 5,
		Documents: []DocumentSummary{{Number: 1, Date: "2026/08/03", PartnerName: "A社", Title: "工事", TotalPrice: 49500, Status: "未設定"}},
	}

	got := formatDocumentList("請求書", list)

	if !strings.Contains(got, "請求書件数: 5件") {
		t.Errorf("total count missing:\n%s", got)
	}
	// limitで打ち切った場合、総件数と表示件数の違いが分かること。
	if !strings.Contains(got, "先頭1件のみ") {
		t.Errorf("truncation notice missing:\n%s", got)
	}
	if !strings.Contains(got, "No.1 2026/08/03 A社 「工事」 49500円 [未設定]") {
		t.Errorf("document line malformed:\n%s", got)
	}
}
