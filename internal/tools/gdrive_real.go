package tools

import (
	"context"
	"fmt"
	"io"
	"strings"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	googleoption "google.golang.org/api/option"
)

// driveScopes はGoogle Drive連携に必要なOAuthスコープです。
// 保存先フォルダを指定した作成を行うには、そのフォルダへのアクセスが必要になるため
// drive.file(アプリが作ったファイルのみ)では足りず、フルスコープを使います。
// 代わりに削除・共有権限変更のツールは公開せず、操作範囲をツールセットで限定しています。
var driveScopes = []string{drive.DriveScope}

// driveFileFields は一覧・取得で必要なフィールドだけを指定します(既定では最小限しか返らない)。
const (
	driveFileFields = "id,name,mimeType,webViewLink,modifiedTime,size"
	driveListFields = "files(" + driveFileFields + ")"
)

// driveExportMimeTypes はGoogleネイティブ形式をテキストとして読むためのエクスポート先です。
var driveExportMimeTypes = map[string]string{
	"application/vnd.google-apps.document":     "text/plain",
	"application/vnd.google-apps.spreadsheet":  "text/csv",
	"application/vnd.google-apps.presentation": "text/plain",
}

// driveTextMimeTypes はエクスポートせずそのままテキストとして読めるMIMEタイプです。
// text/* は前方一致で許可し、ここには text/ で始まらないものだけを列挙します。
var driveTextMimeTypes = map[string]bool{
	"application/json":       true,
	"application/xml":        true,
	"application/javascript": true,
	"application/sql":        true,
	"application/x-yaml":     true,
}

// RealDriveClient はGoogle Workspaceサービスアカウント + ドメイン委任でDrive APIを呼び出す実装です。
// 認証情報はGmail連携と共通(GOOGLE_SERVICE_ACCOUNT_JSON / GOOGLE_IMPERSONATED_USER)で、
// ドメイン委任の許可スコープにDriveスコープを追加しておく必要があります。
type RealDriveClient struct {
	service *drive.Service
}

// NewRealDriveClient はサービスアカウント鍵JSONと代理対象ユーザーのメールアドレスからDriveClientを構築します。
func NewRealDriveClient(ctx context.Context, serviceAccountJSON, impersonatedUser string) (*RealDriveClient, error) {
	cfg, err := google.JWTConfigFromJSON([]byte(serviceAccountJSON), driveScopes...)
	if err != nil {
		return nil, fmt.Errorf("parse service account json: %w", err)
	}
	cfg.Subject = impersonatedUser

	svc, err := drive.NewService(ctx, googleoption.WithHTTPClient(cfg.Client(ctx)))
	if err != nil {
		return nil, fmt.Errorf("create drive service: %w", err)
	}
	return &RealDriveClient{service: svc}, nil
}

func (c *RealDriveClient) SearchFiles(ctx context.Context, keyword, mimeType, folderID string, limit int) ([]DriveFile, error) {
	conditions := []string{"trashed = false"}
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		escaped := escapeDriveQueryValue(keyword)
		conditions = append(conditions, fmt.Sprintf("(name contains '%s' or fullText contains '%s')", escaped, escaped))
	}
	if mimeType != "" {
		conditions = append(conditions, fmt.Sprintf("mimeType = '%s'", escapeDriveQueryValue(mimeType)))
	}
	if folderID != "" {
		conditions = append(conditions, fmt.Sprintf("'%s' in parents", escapeDriveQueryValue(folderID)))
	}

	files, err := c.list(ctx, strings.Join(conditions, " and "), limit)
	if err != nil {
		return nil, fmt.Errorf("search files (keyword=%q): %w", keyword, err)
	}
	return files, nil
}

func (c *RealDriveClient) ListFolder(ctx context.Context, folderID string, limit int) ([]DriveFile, error) {
	if folderID == "" {
		folderID = "root"
	}
	q := fmt.Sprintf("trashed = false and '%s' in parents", escapeDriveQueryValue(folderID))

	files, err := c.list(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list folder %s: %w", folderID, err)
	}
	return files, nil
}

// list はq(Drive APIの検索クエリ)にマッチするファイルを更新日時の降順で返します。
// 共有ドライブ上のファイルも対象にするため SupportsAllDrives / IncludeItemsFromAllDrives を有効にします。
func (c *RealDriveClient) list(ctx context.Context, q string, limit int) ([]DriveFile, error) {
	resp, err := c.service.Files.List().
		Q(q).
		PageSize(int64(normalizeDriveLimit(limit))).
		OrderBy("modifiedTime desc").
		Fields(driveListFields).
		SupportsAllDrives(true).
		IncludeItemsFromAllDrives(true).
		Context(ctx).
		Do()
	if err != nil {
		return nil, err
	}

	files := make([]DriveFile, 0, len(resp.Files))
	for _, f := range resp.Files {
		files = append(files, toDriveFile(f))
	}
	return files, nil
}

func (c *RealDriveClient) ReadFile(ctx context.Context, fileID string, maxChars int) (DriveFileContent, error) {
	meta, err := c.service.Files.Get(fileID).
		Fields(driveFileFields).
		SupportsAllDrives(true).
		Context(ctx).
		Do()
	if err != nil {
		return DriveFileContent{}, fmt.Errorf("get file %s: %w", fileID, err)
	}
	file := toDriveFile(meta)

	body, err := c.download(ctx, file)
	if err != nil {
		return DriveFileContent{}, err
	}
	defer body.Close()

	content, truncated, err := readTruncated(body, normalizeDriveMaxChars(maxChars))
	if err != nil {
		return DriveFileContent{}, fmt.Errorf("read file %s: %w", fileID, err)
	}
	return DriveFileContent{File: file, Content: content, Truncated: truncated}, nil
}

// download はファイル本文のReaderを返します。Googleネイティブ形式はテキスト/CSVへ変換し、
// テキストとして読めない形式は読み取らずにエラーにします(バイナリを本文として渡しても意味がないため)。
func (c *RealDriveClient) download(ctx context.Context, file DriveFile) (io.ReadCloser, error) {
	if exportMimeType, ok := driveExportMimeTypes[file.MimeType]; ok {
		resp, err := c.service.Files.Export(file.ID, exportMimeType).Context(ctx).Download()
		if err != nil {
			return nil, fmt.Errorf("export file %s as %s: %w", file.ID, exportMimeType, err)
		}
		return resp.Body, nil
	}

	if !isDriveTextMimeType(file.MimeType) {
		return nil, fmt.Errorf("ファイル「%s」(%s)はテキストとして読み取れません。URLを案内してください: %s",
			file.Name, file.MimeType, file.URL)
	}

	resp, err := c.service.Files.Get(file.ID).SupportsAllDrives(true).Context(ctx).Download()
	if err != nil {
		return nil, fmt.Errorf("download file %s: %w", file.ID, err)
	}
	return resp.Body, nil
}

func (c *RealDriveClient) CreateDocument(ctx context.Context, name, folderID, body string) (DriveFile, error) {
	meta := &drive.File{
		Name: name,
		// Googleドキュメント形式を指定してtext/plainをアップロードすると、Drive側で変換されます。
		MimeType: "application/vnd.google-apps.document",
	}
	if folderID != "" {
		meta.Parents = []string{folderID}
	}

	created, err := c.service.Files.Create(meta).
		Media(strings.NewReader(body), googleapi.ContentType("text/plain")).
		Fields(driveFileFields).
		SupportsAllDrives(true).
		Context(ctx).
		Do()
	if err != nil {
		return DriveFile{}, fmt.Errorf("create document %q: %w", name, err)
	}
	return toDriveFile(created), nil
}

func toDriveFile(f *drive.File) DriveFile {
	return DriveFile{
		ID:           f.Id,
		Name:         f.Name,
		MimeType:     f.MimeType,
		URL:          f.WebViewLink,
		ModifiedTime: f.ModifiedTime,
		Size:         f.Size,
	}
}

func isDriveTextMimeType(mimeType string) bool {
	// "text/plain; charset=UTF-8" のようにパラメータが付く場合があるため前半だけ見ます。
	base := strings.TrimSpace(strings.Split(mimeType, ";")[0])
	return strings.HasPrefix(base, "text/") || driveTextMimeTypes[base]
}

// escapeDriveQueryValue はDrive APIの検索クエリに埋め込む値をエスケープします。
// 取引先名などにアポストロフィが含まれるとクエリ全体が壊れて400になるため必須です。
func escapeDriveQueryValue(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	return strings.ReplaceAll(v, `'`, `\'`)
}

// readTruncated はrから最大maxChars文字を読み、打ち切ったかどうかを返します。
// 巨大なファイルを丸ごとメモリに載せないよう、読み込むバイト数自体も制限します。
func readTruncated(r io.Reader, maxChars int) (string, bool, error) {
	// UTF-8の1文字は最大4バイト。maxChars文字分に少し余裕を足して読み、超過を検知します。
	limited := io.LimitReader(r, int64(maxChars)*4+8)
	b, err := io.ReadAll(limited)
	if err != nil {
		return "", false, err
	}

	runes := []rune(string(b))
	if len(runes) <= maxChars {
		return string(runes), false, nil
	}
	return string(runes[:maxChars]), true, nil
}
