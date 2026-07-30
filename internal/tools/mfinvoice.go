package tools

import (
	"context"
	"encoding/json"
	"log"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/toolrunner"
)

// InvoiceItem はMoneyForwardクラウド請求書の見積書/請求書に記載する品目です。
type InvoiceItem struct {
	ItemID                 string  `json:"item_id,omitempty"`
	Name                   string  `json:"name"`
	Detail                 string  `json:"detail,omitempty"`
	Unit                   string  `json:"unit,omitempty"`
	Price                  float64 `json:"price"`
	Quantity               float64 `json:"quantity"`
	IsDeductWithholdingTax *bool   `json:"is_deduct_withholding_tax,omitempty"`
	// Excise は税区分。item_idを指定しない場合は必須(例: untaxable, non_taxable, tax_exemption,
	// five_percent, eight_percent, eight_percent_as_reduced_tax_rate, ten_percent)。
	Excise string `json:"excise,omitempty"`
}

// PartnerDepartment は取引先に紐づく部署(請求書/見積書の宛先department_id)です。
type PartnerDepartment struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Partner はMoneyForwardクラウド請求書の取引先です。
type Partner struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Departments []PartnerDepartment `json:"departments"`
}

// InvoiceDocument は作成された見積書/請求書のレスポンス情報です。
type InvoiceDocument struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// MFInvoiceClient はMoneyForwardクラウド請求書APIを抽象化するインターフェースです。
type MFInvoiceClient interface {
	// ListPartners は取引先を名前で検索します(部分一致)。空文字の場合は全件を返します。
	ListPartners(ctx context.Context, name string) ([]Partner, error)
	// CreateEstimate はdepartmentID(取引先の部署ID、ListPartnersで取得)宛の見積書を作成します。
	CreateEstimate(ctx context.Context, departmentID, quoteDate, expiredDate string, items []InvoiceItem) (InvoiceDocument, error)
	// CreateInvoice はdepartmentID宛の請求書を作成します。
	CreateInvoice(ctx context.Context, departmentID, billingDate string, items []InvoiceItem) (InvoiceDocument, error)
}

// MockMFInvoiceClient はMF認証情報が未設定の間に使うスタブ実装です。
type MockMFInvoiceClient struct{}

func NewMockMFInvoiceClient() *MockMFInvoiceClient { return &MockMFInvoiceClient{} }

func (m *MockMFInvoiceClient) ListPartners(ctx context.Context, name string) ([]Partner, error) {
	log.Printf("[MockMFInvoiceClient] ListPartners(name=%s) called (MF認証情報が未設定のためモック応答)", name)
	return []Partner{
		{
			ID:   "mock-partner-1",
			Name: "モック取引先株式会社",
			Departments: []PartnerDepartment{
				{ID: "mock-department-1", Name: "本社"},
			},
		},
	}, nil
}

func (m *MockMFInvoiceClient) CreateEstimate(ctx context.Context, departmentID, quoteDate, expiredDate string, items []InvoiceItem) (InvoiceDocument, error) {
	log.Printf("[MockMFInvoiceClient] CreateEstimate(department=%s, items=%+v) called (MF認証情報が未設定のためモック応答)", departmentID, items)
	return InvoiceDocument{ID: "mock-estimate-1", URL: "https://invoice.moneyforward.com/mock/estimates/1"}, nil
}

func (m *MockMFInvoiceClient) CreateInvoice(ctx context.Context, departmentID, billingDate string, items []InvoiceItem) (InvoiceDocument, error) {
	log.Printf("[MockMFInvoiceClient] CreateInvoice(department=%s, items=%+v) called (MF認証情報が未設定のためモック応答)", departmentID, items)
	return InvoiceDocument{ID: "mock-invoice-1", URL: "https://invoice.moneyforward.com/mock/invoices/1"}, nil
}

// NewMFInvoiceTools はClaudeのFunction CallingからMFInvoiceClientを操作するためのBetaToolセットを構築します。
func NewMFInvoiceTools(client MFInvoiceClient) ([]anthropic.BetaTool, error) {
	itemsSchema := map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"item_id":  map[string]any{"type": "string", "description": "既存マスタ品目のID(任意、省略時はexciseが必須)"},
				"name":     map[string]any{"type": "string", "description": "品目名"},
				"detail":   map[string]any{"type": "string", "description": "品目の詳細(任意)"},
				"unit":     map[string]any{"type": "string", "description": "単位、例: 個(任意)"},
				"price":    map[string]any{"type": "number", "description": "単価(円)"},
				"quantity": map[string]any{"type": "number", "description": "数量"},
				"excise": map[string]any{
					"type":        "string",
					"description": "税区分。item_idを指定しない場合は必須。",
					"enum": []string{
						"untaxable", "non_taxable", "tax_exemption",
						"five_percent", "eight_percent", "eight_percent_as_reduced_tax_rate", "ten_percent",
					},
				},
			},
			"required": []string{"name", "price", "quantity"},
		},
	}

	searchPartnersTool, err := toolrunner.NewBetaToolFromBytes[searchPartnersInput](
		"mf_search_partners",
		"MoneyForwardクラウド請求書の取引先を名前で検索する。見積書・請求書を作成する前に、宛先のdepartment_idを確認するために使う。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "取引先名(部分一致、省略時は全件)"},
			},
		}),
		func(ctx context.Context, in searchPartnersInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			partners, err := client.ListPartners(ctx, in.Name)
			if err != nil {
				return textResult(""), err
			}
			b, err := json.Marshal(partners)
			if err != nil {
				return textResult(""), err
			}
			return textResult(string(b)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	estimateTool, err := toolrunner.NewBetaToolFromBytes[estimateInput](
		"mf_create_estimate",
		"MoneyForwardクラウド請求書で見積書を作成する。department_idはmf_search_partnersで事前に取得しておくこと。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"department_id": map[string]any{"type": "string", "description": "宛先の取引先部署ID(mf_search_partnersで取得)"},
				"quote_date":    map[string]any{"type": "string", "description": "見積日、YYYY-MM-DD形式"},
				"expired_date":  map[string]any{"type": "string", "description": "有効期限、YYYY-MM-DD形式"},
				"items":         itemsSchema,
			},
			"required": []string{"department_id", "quote_date", "expired_date", "items"},
		}),
		func(ctx context.Context, in estimateInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			doc, err := client.CreateEstimate(ctx, in.DepartmentID, in.QuoteDate, in.ExpiredDate, in.Items)
			if err != nil {
				return textResult(""), err
			}
			return marshalInvoiceResult(doc)
		},
	)
	if err != nil {
		return nil, err
	}

	invoiceTool, err := toolrunner.NewBetaToolFromBytes[billingInput](
		"mf_create_invoice",
		"MoneyForwardクラウド請求書で請求書を作成する。department_idはmf_search_partnersで事前に取得しておくこと。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"department_id": map[string]any{"type": "string", "description": "宛先の取引先部署ID(mf_search_partnersで取得)"},
				"billing_date":  map[string]any{"type": "string", "description": "請求日、YYYY-MM-DD形式"},
				"items":         itemsSchema,
			},
			"required": []string{"department_id", "billing_date", "items"},
		}),
		func(ctx context.Context, in billingInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			doc, err := client.CreateInvoice(ctx, in.DepartmentID, in.BillingDate, in.Items)
			if err != nil {
				return textResult(""), err
			}
			return marshalInvoiceResult(doc)
		},
	)
	if err != nil {
		return nil, err
	}

	return []anthropic.BetaTool{searchPartnersTool, estimateTool, invoiceTool}, nil
}

func marshalInvoiceResult(doc InvoiceDocument) (anthropic.BetaToolResultBlockParamContentUnion, error) {
	b, err := json.Marshal(doc)
	if err != nil {
		return textResult(""), err
	}
	return textResult(string(b)), nil
}

type searchPartnersInput struct {
	Name string `json:"name,omitempty"`
}

type estimateInput struct {
	DepartmentID string        `json:"department_id"`
	QuoteDate    string        `json:"quote_date"`
	ExpiredDate  string        `json:"expired_date"`
	Items        []InvoiceItem `json:"items"`
}

type billingInput struct {
	DepartmentID string        `json:"department_id"`
	BillingDate  string        `json:"billing_date"`
	Items        []InvoiceItem `json:"items"`
}
