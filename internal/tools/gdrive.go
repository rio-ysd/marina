package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/toolrunner"
)

// DriveFile はGoogle Drive上のファイル/フォルダの要約情報です。
type DriveFile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MimeType string `json:"mime_type"`
	// URL はブラウザで開けるリンク(webViewLink)です。Slackへ案内する際に使います。
	URL          string `json:"url,omitempty"`
	ModifiedTime string `json:"modified_time,omitempty"`
	// Size はバイト数。Googleドキュメント等のネイティブ形式は0になります。
	Size int64 `json:"size,omitempty"`
}

// IsFolder はフォルダかどうかを返します。
func (f DriveFile) IsFolder() bool { return f.MimeType == driveFolderMimeType }

// DriveFileContent はファイル本文の取得結果です。
// Truncatedがtrueの場合、Contentはmax_charsで打ち切られています。
type DriveFileContent struct {
	File      DriveFile `json:"file"`
	Content   string    `json:"content"`
	Truncated bool      `json:"truncated"`
}

// 一覧・本文取得の既定値。ツールの結果はそのままClaudeの入力になるため、
// 際限なく詰め込まないよう既定値と上限を持たせています。
const (
	driveFolderMimeType  = "application/vnd.google-apps.folder"
	defaultDriveLimit    = 20
	maxDriveLimit        = 100
	defaultDriveMaxChars = 10000
	maxDriveMaxChars     = 50000
)

func normalizeDriveLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultDriveLimit
	case limit > maxDriveLimit:
		return maxDriveLimit
	default:
		return limit
	}
}

func normalizeDriveMaxChars(maxChars int) int {
	switch {
	case maxChars <= 0:
		return defaultDriveMaxChars
	case maxChars > maxDriveMaxChars:
		return maxDriveMaxChars
	default:
		return maxChars
	}
}

// DriveClient はGoogle Drive連携に必要な操作を抽象化するインターフェースです。
// 削除・共有権限の変更は取り返しがつかないため、意図的に公開していません。
type DriveClient interface {
	// SearchFiles はファイル名と本文(全文検索)にkeywordを含むファイルを返します。
	// mimeTypeが空でなければ種類で絞り、folderIDが空でなければそのフォルダ直下に限定します。
	SearchFiles(ctx context.Context, keyword, mimeType, folderID string, limit int) ([]DriveFile, error)
	// ListFolder は指定フォルダ直下のファイル/サブフォルダを返します。
	ListFolder(ctx context.Context, folderID string, limit int) ([]DriveFile, error)
	// ReadFile はファイル本文をテキストとして返します。
	// Googleドキュメント/スプレッドシート/スライドはテキスト・CSVへ変換して返します。
	ReadFile(ctx context.Context, fileID string, maxChars int) (DriveFileContent, error)
	// CreateDocument はGoogleドキュメントをbodyの内容で作成します。folderIDが空ならマイドライブ直下です。
	CreateDocument(ctx context.Context, name, folderID, body string) (DriveFile, error)
}

// MockDriveClient はGoogle認証情報が未設定の間に使うスタブ実装です。
type MockDriveClient struct{}

func NewMockDriveClient() *MockDriveClient { return &MockDriveClient{} }

func (m *MockDriveClient) SearchFiles(ctx context.Context, keyword, mimeType, folderID string, limit int) ([]DriveFile, error) {
	log.Printf("[MockDriveClient] SearchFiles(keyword=%s, mimeType=%s, folderID=%s) called (Google認証情報が未設定のためモック応答)",
		keyword, mimeType, folderID)
	return []DriveFile{{
		ID:           "mock-file-1",
		Name:         "[サンプル] 議事録 2026-08-01",
		MimeType:     "application/vnd.google-apps.document",
		URL:          "https://docs.google.com/document/d/mock-file-1/edit",
		ModifiedTime: "2026-08-01T09:00:00.000Z",
	}}, nil
}

func (m *MockDriveClient) ListFolder(ctx context.Context, folderID string, limit int) ([]DriveFile, error) {
	log.Printf("[MockDriveClient] ListFolder(folderID=%s) called (Google認証情報が未設定のためモック応答)", folderID)
	return []DriveFile{{
		ID:       "mock-folder-1",
		Name:     "[サンプル] 見積書",
		MimeType: driveFolderMimeType,
		URL:      "https://drive.google.com/drive/folders/mock-folder-1",
	}}, nil
}

func (m *MockDriveClient) ReadFile(ctx context.Context, fileID string, maxChars int) (DriveFileContent, error) {
	log.Printf("[MockDriveClient] ReadFile(fileID=%s) called (Google認証情報が未設定のためモック応答)", fileID)
	return DriveFileContent{
		File:    DriveFile{ID: fileID, Name: "[サンプル] 議事録 2026-08-01", MimeType: "application/vnd.google-apps.document"},
		Content: "これはGoogle Drive連携未設定時のモックデータです。",
	}, nil
}

func (m *MockDriveClient) CreateDocument(ctx context.Context, name, folderID, body string) (DriveFile, error) {
	log.Printf("[MockDriveClient] CreateDocument(name=%s, folderID=%s) called (Google認証情報が未設定のためモック応答)", name, folderID)
	return DriveFile{
		ID:       "mock-created-1",
		Name:     name,
		MimeType: "application/vnd.google-apps.document",
		URL:      "https://docs.google.com/document/d/mock-created-1/edit",
	}, nil
}

// NewDriveTools はClaudeのFunction CallingからDriveClientを操作するためのBetaToolセットを構築します。
func NewDriveTools(client DriveClient) ([]anthropic.BetaTool, error) {
	searchTool, err := toolrunner.NewBetaToolFromBytes[driveSearchInput](
		"drive_search_files",
		"Google Driveのファイルをキーワードで検索する。ファイル名と本文(全文検索)の両方が対象。"+
			"「〇〇の資料どこ?」「〇〇について書いてある資料」等に使う。"+
			"本文を読みたい場合は、検索で得たidをdrive_read_fileに渡す。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"keyword": map[string]any{"type": "string", "description": "検索キーワード(ファイル名と本文の部分一致)"},
				"file_type": map[string]any{
					"type":        "string",
					"description": "ファイル種別で絞り込む(任意)。folder=フォルダ、document=Googleドキュメント、spreadsheet=スプレッドシート、presentation=スライド、pdf=PDF",
					"enum":        []string{"folder", "document", "spreadsheet", "presentation", "pdf"},
				},
				"folder_id": map[string]any{"type": "string", "description": "このフォルダ直下に限定する(任意)。フォルダIDはdrive_search_filesでfile_type=folderとして検索して得る"},
				"limit":     map[string]any{"type": "integer", "description": "最大件数(既定20、最大100)"},
			},
			"required": []string{"keyword"},
		}),
		func(ctx context.Context, in driveSearchInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			files, err := client.SearchFiles(ctx, in.Keyword, driveMimeTypeFor(in.FileType), in.FolderID, in.Limit)
			if err != nil {
				return textResult(""), err
			}
			return textResult(formatDriveFiles(fmt.Sprintf("「%s」の検索結果", in.Keyword), files)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	listTool, err := toolrunner.NewBetaToolFromBytes[driveListInput](
		"drive_list_folder",
		"Google Driveの指定フォルダ直下にあるファイル/サブフォルダの一覧を取得する。"+
			"folder_idを省略するとマイドライブ直下を返す。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"folder_id": map[string]any{"type": "string", "description": "フォルダID(省略時はマイドライブ直下)"},
				"limit":     map[string]any{"type": "integer", "description": "最大件数(既定20、最大100)"},
			},
		}),
		func(ctx context.Context, in driveListInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			files, err := client.ListFolder(ctx, in.FolderID, in.Limit)
			if err != nil {
				return textResult(""), err
			}
			return textResult(formatDriveFiles("フォルダの中身", files)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	readTool, err := toolrunner.NewBetaToolFromBytes[driveReadInput](
		"drive_read_file",
		"Google Driveのファイル本文をテキストとして読み取る。"+
			"Googleドキュメント/スプレッドシート/スライドとテキスト形式のファイルに対応する"+
			"(PDF・画像・Officeファイルなどは読めないため、その場合はリンクを案内すること)。"+
			"file_idはdrive_search_filesまたはdrive_list_folderで取得しておくこと。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_id":   map[string]any{"type": "string", "description": "読み取るファイルのID"},
				"max_chars": map[string]any{"type": "integer", "description": "取得する最大文字数(既定10000、最大50000)。超過分は打ち切られる"},
			},
			"required": []string{"file_id"},
		}),
		func(ctx context.Context, in driveReadInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			content, err := client.ReadFile(ctx, in.FileID, in.MaxChars)
			if err != nil {
				return textResult(""), err
			}
			return textResult(formatDriveContent(content)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	createTool, err := toolrunner.NewBetaToolFromBytes[driveCreateInput](
		"drive_create_document",
		"Google Driveにテキスト内容のGoogleドキュメントを新規作成する。議事録やメモの保存に使う。"+
			"保存先フォルダを指定する場合は、事前にdrive_search_files(file_type=folder)でfolder_idを確認すること。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":      map[string]any{"type": "string", "description": "作成するドキュメントのタイトル"},
				"body":      map[string]any{"type": "string", "description": "ドキュメントの本文(プレーンテキスト)"},
				"folder_id": map[string]any{"type": "string", "description": "保存先フォルダID(省略時はマイドライブ直下)"},
			},
			"required": []string{"name", "body"},
		}),
		func(ctx context.Context, in driveCreateInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			file, err := client.CreateDocument(ctx, in.Name, in.FolderID, in.Body)
			if err != nil {
				return textResult(""), err
			}
			b, err := json.Marshal(file)
			if err != nil {
				return textResult(""), err
			}
			return textResult(string(b)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	return []anthropic.BetaTool{searchTool, listTool, readTool, createTool}, nil
}

// driveMimeTypeFor はツール入力の分かりやすい種別名をMIMEタイプへ変換します。
// Claudeに生のMIMEタイプを書かせると誤字で0件になるため、enumで受けてここで解決します。
func driveMimeTypeFor(fileType string) string {
	switch fileType {
	case "folder":
		return driveFolderMimeType
	case "document":
		return "application/vnd.google-apps.document"
	case "spreadsheet":
		return "application/vnd.google-apps.spreadsheet"
	case "presentation":
		return "application/vnd.google-apps.presentation"
	case "pdf":
		return "application/pdf"
	default:
		return ""
	}
}

// formatDriveFiles は一覧をClaudeが読みやすいテキストにまとめます。
// idは後続ツールの入力になるため必ず含めます。
func formatDriveFiles(title string, files []DriveFile) string {
	if len(files) == 0 {
		return fmt.Sprintf("%s: 0件(該当するファイルはありません)", title)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d件\n", title, len(files))
	for _, f := range files {
		kind := "ファイル"
		if f.IsFolder() {
			kind = "フォルダ"
		}
		fmt.Fprintf(&b, "- [%s] %s (id: %s, 更新: %s)", kind, f.Name, f.ID, f.ModifiedTime)
		if f.URL != "" {
			fmt.Fprintf(&b, " %s", f.URL)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func formatDriveContent(c DriveFileContent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ファイル: %s (id: %s)\n", c.File.Name, c.File.ID)
	if c.File.URL != "" {
		fmt.Fprintf(&b, "URL: %s\n", c.File.URL)
	}
	if c.Truncated {
		fmt.Fprintf(&b, "(本文が長いため先頭%d文字のみ。続きが必要ならmax_charsを増やして再取得)\n", len([]rune(c.Content)))
	}
	b.WriteString("---\n")
	b.WriteString(c.Content)
	return b.String()
}

type driveSearchInput struct {
	Keyword  string `json:"keyword"`
	FileType string `json:"file_type,omitempty"`
	FolderID string `json:"folder_id,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type driveListInput struct {
	FolderID string `json:"folder_id,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type driveReadInput struct {
	FileID   string `json:"file_id"`
	MaxChars int    `json:"max_chars,omitempty"`
}

type driveCreateInput struct {
	Name     string `json:"name"`
	Body     string `json:"body"`
	FolderID string `json:"folder_id,omitempty"`
}
