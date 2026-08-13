package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

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

// 一覧取得の既定値。Lambdaのタイムゾーンは UTC のため、「今月」はJST基準で解決します。
const (
	defaultListLimit = 20
	maxListLimit     = 100
	mfDateLayout     = "2006-01-02"
)

var jst = time.FixedZone("JST", 9*60*60)

// normalizeRange はfrom/toが空の場合に「今月(JST)」の範囲を補います。
// 片方だけ指定された場合はもう片方を今月の端で補完します。
func normalizeRange(from, to string) (string, string) {
	if from != "" && to != "" {
		return from, to
	}
	now := time.Now().In(jst)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, jst)
	monthEnd := monthStart.AddDate(0, 1, -1)
	if from == "" {
		from = monthStart.Format(mfDateLayout)
	}
	if to == "" {
		to = monthEnd.Format(mfDateLayout)
	}
	return from, to
}

func normalizeLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultListLimit
	case limit > maxListLimit:
		return maxListLimit
	default:
		return limit
	}
}

// DocumentSummary は一覧取得で返す見積書/請求書の要約です。
type DocumentSummary struct {
	ID          string  `json:"id"`
	Number      int     `json:"number"`
	Date        string  `json:"date"`
	PartnerName string  `json:"partner_name"`
	Title       string  `json:"title"`
	TotalPrice  float64 `json:"total_price"`
	// Status は請求書なら入金ステータス、見積書なら受注ステータスです。
	Status string `json:"status"`
	PDFURL string `json:"pdf_url"`
}

// dateTypeLabel は日付基準を日本語ラベルにします。どの日付で絞ったかが分からないと
// 「今月の請求書」と「今月が支払期限の請求書」を取り違えるため、結果に明示します。
func dateTypeLabel(dateType string) string {
	switch dateType {
	case "due_date":
		return "支払期限"
	case "sales_date":
		return "売上日"
	case "created_at":
		return "作成日"
	case "expired_date":
		return "有効期限"
	case "quote_date":
		return "見積日"
	default:
		return "請求日"
	}
}

// DocumentList は一覧取得の結果です。TotalCountは期間内の総件数で、
// Documentsは上限(limit)までの明細なので、件数だけを聞かれた場合はTotalCountを使います。
type DocumentList struct {
	// DateType は絞り込みに使った日付の種類(billing_date / due_date など)です。
	DateType   string            `json:"date_type"`
	From       string            `json:"from"`
	To         string            `json:"to"`
	TotalCount int               `json:"total_count"`
	Documents  []DocumentSummary `json:"documents"`
}

// MFInvoiceClient はMoneyForwardクラウド請求書APIを抽象化するインターフェースです。
type MFInvoiceClient interface {
	// ListPartners は取引先を名前で検索します(部分一致)。空文字の場合は全件を返します。
	ListPartners(ctx context.Context, name string) ([]Partner, error)
	// ListInvoices はdateType(請求日/支払期限など)がfrom〜toの範囲にある請求書を返します。
	// 日付はYYYY-MM-DD、空なら今月。dateTypeが空なら請求日(billing_date)基準です。
	ListInvoices(ctx context.Context, dateType, from, to string, limit int) (DocumentList, error)
	// ListEstimates はdateType(見積日/有効期限)がfrom〜toの範囲にある見積書を返します。
	ListEstimates(ctx context.Context, dateType, from, to string, limit int) (DocumentList, error)
	// CreateEstimate はdepartmentID(取引先の部署ID、ListPartnersで取得)宛の見積書を作成します。
	CreateEstimate(ctx context.Context, departmentID, quoteDate, expiredDate string, items []InvoiceItem) (InvoiceDocument, error)
	// CreateInvoice はdepartmentID宛の請求書を作成します。
	CreateInvoice(ctx context.Context, departmentID, billingDate string, items []InvoiceItem) (InvoiceDocument, error)
}

// MockMFInvoiceClient はMF認証情報が未設定の間に使うスタブ実装です。
type MockMFInvoiceClient struct{}

func NewMockMFInvoiceClient() *MockMFInvoiceClient { return &MockMFInvoiceClient{} }

func (m *MockMFInvoiceClient) ListInvoices(ctx context.Context, dateType, from, to string, limit int) (DocumentList, error) {
	log.Printf("[MockMFInvoiceClient] ListInvoices(dateType=%s, from=%s, to=%s) called (MF認証情報が未設定のためモック応答)", dateType, from, to)
	return mockDocumentList(from, to, "請求書"), nil
}

func (m *MockMFInvoiceClient) ListEstimates(ctx context.Context, dateType, from, to string, limit int) (DocumentList, error) {
	log.Printf("[MockMFInvoiceClient] ListEstimates(dateType=%s, from=%s, to=%s) called (MF認証情報が未設定のためモック応答)", dateType, from, to)
	return mockDocumentList(from, to, "見積書"), nil
}

func mockDocumentList(from, to, kind string) DocumentList {
	from, to = normalizeRange(from, to)
	return DocumentList{
		From:       from,
		To:         to,
		TotalCount: 1,
		Documents: []DocumentSummary{{
			ID: "mock-1", Number: 1, Date: from, PartnerName: "モック株式会社",
			Title: "モック" + kind, TotalPrice: 110000, Status: "未設定",
		}},
	}
}

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

	listRangeSchema := func(dateTypeDescription string, dateTypes []string) []byte {
		return mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"date_type": map[string]any{
					"type":        "string",
					"description": dateTypeDescription,
					"enum":        dateTypes,
				},
				"from":  map[string]any{"type": "string", "description": "期間の開始日(YYYY-MM-DD)。省略時は今月の初日"},
				"to":    map[string]any{"type": "string", "description": "期間の終了日(YYYY-MM-DD)。省略時は今月の末日"},
				"limit": map[string]any{"type": "integer", "description": "一覧に含める最大件数(既定20、最大100)。件数だけ知りたい場合も総件数は別途返る"},
			},
		})
	}

	listInvoicesTool, err := toolrunner.NewBetaToolFromBytes[listDocumentsInput](
		"mf_list_invoices",
		"MoneyForwardクラウド請求書の請求書を期間で検索し、総件数と一覧を返す。"+
			"「今月の請求書は何通か」「今月末が支払期限の請求書」等に使う。"+
			"date_typeで日付の基準を選べる(既定は請求日)。支払期限で絞るならdue_dateを指定する。",
		listRangeSchema(
			"日付の基準。billing_date=請求日(既定)、due_date=支払期限、sales_date=売上日、created_at=作成日",
			[]string{"billing_date", "due_date", "sales_date", "created_at"},
		),
		func(ctx context.Context, in listDocumentsInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			list, err := client.ListInvoices(ctx, in.DateType, in.From, in.To, in.Limit)
			if err != nil {
				return textResult(""), err
			}
			return textResult(formatDocumentList("請求書", list)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	listEstimatesTool, err := toolrunner.NewBetaToolFromBytes[listDocumentsInput](
		"mf_list_estimates",
		"MoneyForwardクラウド請求書の見積書を期間で検索し、総件数と一覧を返す。"+
			"date_typeで日付の基準を選べる(既定は見積日)。有効期限で絞るならexpired_dateを指定する。",
		listRangeSchema(
			"日付の基準。quote_date=見積日(既定)、expired_date=有効期限",
			[]string{"quote_date", "expired_date"},
		),
		func(ctx context.Context, in listDocumentsInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			list, err := client.ListEstimates(ctx, in.DateType, in.From, in.To, in.Limit)
			if err != nil {
				return textResult(""), err
			}
			return textResult(formatDocumentList("見積書", list)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	return []anthropic.BetaTool{
		searchPartnersTool, estimateTool, invoiceTool,
		listInvoicesTool, listEstimatesTool,
	}, nil
}

type listDocumentsInput struct {
	DateType string `json:"date_type"`
	From     string `json:"from"`
	To       string `json:"to"`
	Limit    int    `json:"limit"`
}

// formatDocumentList は一覧をClaudeが読みやすいテキストにまとめます。
// 総件数と表示件数が異なる場合(limitで打ち切った場合)はその旨を明示します。
func formatDocumentList(kind string, list DocumentList) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s(%s): %s〜%s / %s件数: %d件\n",
		kind, dateTypeLabel(list.DateType), list.From, list.To, kind, list.TotalCount)
	if list.TotalCount > len(list.Documents) {
		fmt.Fprintf(&b, "(以下は先頭%d件のみ。総件数は上記の%d件)\n", len(list.Documents), list.TotalCount)
	}
	for _, d := range list.Documents {
		fmt.Fprintf(&b, "- No.%d %s %s 「%s」 %.0f円 [%s]\n",
			d.Number, d.Date, d.PartnerName, d.Title, d.TotalPrice, d.Status)
	}
	return b.String()
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
