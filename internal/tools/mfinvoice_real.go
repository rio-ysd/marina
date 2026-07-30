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
	"time"

	"golang.org/x/oauth2"

	"github.com/yoshida-rio/marina/internal/storage"
)

const (
	mfInvoiceAPIBaseURL = "https://invoice.moneyforward.com/api/v3"
	mfOAuthProvider     = "moneyforward"
	mfOAuthAccount      = "default"
)

// RealMFInvoiceClient はMoneyForwardクラウド請求書API(v3)をOAuth2経由で呼び出す実装です。
// アクセストークンはDBに保存されたリフレッシュトークンから必要に応じて自動更新されます。
type RealMFInvoiceClient struct {
	oauthConfig *oauth2.Config
	tokenRepo   *storage.OAuthTokenRepo
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
