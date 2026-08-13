package tools

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/yoshida-rio/marina/internal/storage"
)

const (
	mfInvoiceAPIBaseURL = "https://invoice.moneyforward.com/api/v3"
	mfOAuthProvider     = "moneyforward"
	mfOAuthAccount      = "default"
)

// mfTokenStore はOAuthトークンの保存先です。テストでDBを使わずに差し替えられるよう
// インターフェースにしています(本番は*storage.OAuthTokenRepoがそのまま満たします)。
type mfTokenStore interface {
	GetByProviderAccount(ctx context.Context, provider, account string) (*storage.OAuthToken, error)
	Upsert(ctx context.Context, provider, account, accessToken, refreshToken string, expiresAt *time.Time) error
}

// RealMFInvoiceClient はMoneyForwardクラウド請求書API(v3)をOAuth2経由で呼び出す実装です。
// アクセストークンはDBに保存されたリフレッシュトークンから必要に応じて自動更新されます。
type RealMFInvoiceClient struct {
	oauthConfig *oauth2.Config
	tokenRepo   mfTokenStore
	httpClient  *http.Client
	baseURL     string
}

// NewRealMFInvoiceClient はRealMFInvoiceClientを構築します。
func NewRealMFInvoiceClient(oauthConfig *oauth2.Config, tokenRepo *storage.OAuthTokenRepo) *RealMFInvoiceClient {
	return &RealMFInvoiceClient{
		oauthConfig: oauthConfig,
		tokenRepo:   tokenRepo,
		httpClient:  http.DefaultClient,
		baseURL:     mfInvoiceAPIBaseURL,
	}
}

// mfPartnersResponse は GET /partners のレスポンス形式です。
type mfPartnersResponse struct {
	Data []mfPartner `json:"data"`
}

type mfPartner struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Departments []mfPartnerDeptRaw `json:"departments"`
}

type mfPartnerDeptRaw struct {
	ID string `json:"id"`
}

// mfDocumentResponse は見積書/請求書作成レスポンスの共通部分です。
type mfDocumentResponse struct {
	ID     string `json:"id"`
	PDFURL string `json:"pdf_url"`
}

// mfListResponse は GET /billings・GET /quotes のレスポンス形式です。
// 請求書と見積書で日付・番号・ステータスのキーが異なるため、両方を受けて後段で振り分けます。
type mfListResponse struct {
	Data       []mfListItem `json:"data"`
	Pagination struct {
		TotalCount int `json:"total_count"`
	} `json:"pagination"`
}

type mfListItem struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	PartnerName   string   `json:"partner_name"`
	TotalPrice    mfNumber `json:"total_price"`
	PDFURL        string   `json:"pdf_url"`
	BillingDate   string   `json:"billing_date"`
	BillingNumber mfNumber `json:"billing_number"`
	PaymentStatus string   `json:"payment_status"`
	QuoteDate     string   `json:"quote_date"`
	QuoteNumber   mfNumber `json:"quote_number"`
	OrderStatus   string   `json:"order_status"`
}

// mfNumber は数値にも文字列にもなりうるフィールドを受けるための型です。
// 請求書APIは total_price や billing_number を "49500.0" のように文字列で返すことがあり、
// float64で受けるとデコードエラーで一覧取得ごと失敗するため、両方を許容します。
type mfNumber float64

func (n *mfNumber) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("parse number %s: %w", string(b), err)
	}
	*n = mfNumber(f)
	return nil
}

func (c *RealMFInvoiceClient) ListInvoices(ctx context.Context, from, to string, limit int) (DocumentList, error) {
	return c.list(ctx, "/billings", "billing_date", from, to, limit)
}

func (c *RealMFInvoiceClient) ListEstimates(ctx context.Context, from, to string, limit int) (DocumentList, error) {
	return c.list(ctx, "/quotes", "quote_date", from, to, limit)
}

// list は請求書/見積書の一覧を取得します。
// APIはfrom/toを単独で渡すと422になり、range_key(日付の基準となる項目)とセットで渡す必要があります。
func (c *RealMFInvoiceClient) list(ctx context.Context, path, rangeKey, from, to string, limit int) (DocumentList, error) {
	from, to = normalizeRange(from, to)
	limit = normalizeLimit(limit)

	query := url.Values{}
	query.Set("range_key", rangeKey)
	query.Set("from", from)
	query.Set("to", to)
	query.Set("per_page", fmt.Sprintf("%d", limit))
	query.Set("page", "1")

	var resp mfListResponse
	if err := c.do(ctx, http.MethodGet, path, query, nil, &resp); err != nil {
		return DocumentList{}, err
	}

	result := DocumentList{From: from, To: to, TotalCount: resp.Pagination.TotalCount}
	for _, item := range resp.Data {
		summary := DocumentSummary{
			ID:          item.ID,
			Title:       item.Title,
			PartnerName: item.PartnerName,
			TotalPrice:  float64(item.TotalPrice),
			PDFURL:      item.PDFURL,
		}
		if rangeKey == "billing_date" {
			summary.Date, summary.Number, summary.Status = item.BillingDate, int(item.BillingNumber), item.PaymentStatus
		} else {
			summary.Date, summary.Number, summary.Status = item.QuoteDate, int(item.QuoteNumber), item.OrderStatus
		}
		result.Documents = append(result.Documents, summary)
	}
	return result, nil
}

func (c *RealMFInvoiceClient) ListPartners(ctx context.Context, name string) ([]Partner, error) {
	query := url.Values{}
	if name != "" {
		query.Set("name", name)
	}

	var resp mfPartnersResponse
	if err := c.do(ctx, http.MethodGet, "/partners", query, nil, &resp); err != nil {
		return nil, err
	}

	partners := make([]Partner, 0, len(resp.Data))
	for _, p := range resp.Data {
		departments := make([]PartnerDepartment, 0, len(p.Departments))
		for _, d := range p.Departments {
			departments = append(departments, PartnerDepartment{ID: d.ID})
		}
		partners = append(partners, Partner{ID: p.ID, Name: p.Name, Departments: departments})
	}
	return partners, nil
}

func (c *RealMFInvoiceClient) CreateEstimate(ctx context.Context, departmentID, quoteDate, expiredDate string, items []InvoiceItem) (InvoiceDocument, error) {
	body := map[string]any{
		"department_id": departmentID,
		"quote_date":    quoteDate,
		"expired_date":  expiredDate,
		"items":         items,
	}

	var resp mfDocumentResponse
	if err := c.do(ctx, http.MethodPost, "/quotes", nil, body, &resp); err != nil {
		return InvoiceDocument{}, err
	}
	return InvoiceDocument{ID: resp.ID, URL: resp.PDFURL}, nil
}

func (c *RealMFInvoiceClient) CreateInvoice(ctx context.Context, departmentID, billingDate string, items []InvoiceItem) (InvoiceDocument, error) {
	body := map[string]any{
		"department_id": departmentID,
		"billing_date":  billingDate,
		"items":         items,
	}

	var resp mfDocumentResponse
	if err := c.do(ctx, http.MethodPost, "/invoice_template_billings", nil, body, &resp); err != nil {
		return InvoiceDocument{}, err
	}
	return InvoiceDocument{ID: resp.ID, URL: resp.PDFURL}, nil
}

// do はMoneyForwardクラウド請求書APIへ認可済みリクエストを送り、レスポンスJSONをoutにデコードします。
func (c *RealMFInvoiceClient) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	accessToken, err := c.accessToken(ctx)
	if err != nil {
		return err
	}

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	reqURL := c.baseURL + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, reqBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("call moneyforward invoice api: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("moneyforward invoice api returned status %d: %s", resp.StatusCode, string(respBody))
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response body: %w", err)
		}
	}
	return nil
}

// accessToken はDBに保存されたトークンから有効なアクセストークンを返します。
// 期限切れの場合はrefresh_tokenを使って自動更新し、更新結果をDBに保存します。
func (c *RealMFInvoiceClient) accessToken(ctx context.Context) (string, error) {
	stored, err := c.tokenRepo.GetByProviderAccount(ctx, mfOAuthProvider, mfOAuthAccount)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("moneyforwardの認可が完了していません。/mf/oauth/startにアクセスして認可してください")
		}
		return "", fmt.Errorf("load moneyforward oauth token: %w", err)
	}

	token := &oauth2.Token{
		AccessToken:  stored.AccessToken,
		RefreshToken: stored.RefreshToken,
	}
	if stored.ExpiresAt != nil {
		token.Expiry = *stored.ExpiresAt
	}

	fresh, err := c.oauthConfig.TokenSource(ctx, token).Token()
	if err != nil {
		return "", fmt.Errorf("refresh moneyforward oauth token: %w", err)
	}

	if fresh.AccessToken != token.AccessToken {
		var expiresAt *time.Time
		if !fresh.Expiry.IsZero() {
			expiresAt = &fresh.Expiry
		}
		if err := c.tokenRepo.Upsert(ctx, mfOAuthProvider, mfOAuthAccount, fresh.AccessToken, fresh.RefreshToken, expiresAt); err != nil {
			return "", fmt.Errorf("save refreshed moneyforward oauth token: %w", err)
		}
	}

	return fresh.AccessToken, nil
}
