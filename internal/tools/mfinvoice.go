package tools

import (
	"context"
	"encoding/json"
	"log"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/toolrunner"
)

// InvoiceItem はMoneyForwardクラウド会計の見積書/請求書に記載する品目です。
type InvoiceItem struct {
	Name     string `json:"name"`
	Quantity int64  `json:"quantity"`
	UnitPrice int64 `json:"unit_price"`
}

// InvoiceDocument は作成された見積書/請求書のレスポンス情報です。
type InvoiceDocument struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// MFInvoiceClient はMoneyForwardクラウド会計の見積書/請求書APIを抽象化するインターフェースです。
// OAuthクレデンシャルが未確定のため、確定後にHTTPクライアント実装を追加してください。
type MFInvoiceClient interface {
	CreateEstimate(ctx context.Context, partnerName string, items []InvoiceItem) (InvoiceDocument, error)
	CreateInvoice(ctx context.Context, partnerName string, items []InvoiceItem) (InvoiceDocument, error)
}

// MockMFInvoiceClient はMF認証情報が未設定の間に使うスタブ実装です。
type MockMFInvoiceClient struct{}

func NewMockMFInvoiceClient() *MockMFInvoiceClient { return &MockMFInvoiceClient{} }

func (m *MockMFInvoiceClient) CreateEstimate(ctx context.Context, partnerName string, items []InvoiceItem) (InvoiceDocument, error) {
	log.Printf("[MockMFInvoiceClient] CreateEstimate(partner=%s, items=%+v) called (MF認証情報が未設定のためモック応答)", partnerName, items)
	return InvoiceDocument{ID: "mock-estimate-1", URL: "https://invoice.moneyforward.com/mock/estimates/1"}, nil
}

func (m *MockMFInvoiceClient) CreateInvoice(ctx context.Context, partnerName string, items []InvoiceItem) (InvoiceDocument, error) {
	log.Printf("[MockMFInvoiceClient] CreateInvoice(partner=%s, items=%+v) called (MF認証情報が未設定のためモック応答)", partnerName, items)
	return InvoiceDocument{ID: "mock-invoice-1", URL: "https://invoice.moneyforward.com/mock/invoices/1"}, nil
}

// NewMFInvoiceTools はClaudeのFunction CallingからMFInvoiceClientを操作するためのBetaToolセットを構築します。
func NewMFInvoiceTools(client MFInvoiceClient) ([]anthropic.BetaTool, error) {
	itemsSchema := map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":       map[string]any{"type": "string", "description": "品目名"},
				"quantity":   map[string]any{"type": "integer", "description": "数量"},
				"unit_price": map[string]any{"type": "integer", "description": "単価(円)"},
			},
			"required": []string{"name", "quantity", "unit_price"},
		},
	}

	estimateTool, err := toolrunner.NewBetaToolFromBytes[invoiceInput](
		"mf_create_estimate",
		"MoneyForwardクラウド会計で見積書を作成する。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"partner_name": map[string]any{"type": "string", "description": "取引先名"},
				"items":        itemsSchema,
			},
			"required": []string{"partner_name", "items"},
		}),
		func(ctx context.Context, in invoiceInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			doc, err := client.CreateEstimate(ctx, in.PartnerName, in.Items)
			if err != nil {
				return textResult(""), err
			}
			return marshalInvoiceResult(doc)
		},
	)
	if err != nil {
		return nil, err
	}

	invoiceTool, err := toolrunner.NewBetaToolFromBytes[invoiceInput](
		"mf_create_invoice",
		"MoneyForwardクラウド会計で請求書を作成する。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"partner_name": map[string]any{"type": "string", "description": "取引先名"},
				"items":        itemsSchema,
			},
			"required": []string{"partner_name", "items"},
		}),
		func(ctx context.Context, in invoiceInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			doc, err := client.CreateInvoice(ctx, in.PartnerName, in.Items)
			if err != nil {
				return textResult(""), err
			}
			return marshalInvoiceResult(doc)
		},
	)
	if err != nil {
		return nil, err
	}

	return []anthropic.BetaTool{estimateTool, invoiceTool}, nil
}

func marshalInvoiceResult(doc InvoiceDocument) (anthropic.BetaToolResultBlockParamContentUnion, error) {
	b, err := json.Marshal(doc)
	if err != nil {
		return textResult(""), err
	}
	return textResult(string(b)), nil
}

type invoiceInput struct {
	PartnerName string        `json:"partner_name"`
	Items       []InvoiceItem `json:"items"`
}
