package tools

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"google.golang.org/api/drive/v3"
	googleoption "google.golang.org/api/option"
)

// newTestDriveClient はテスト用HTTPサーバーを向いた実クライアントを構築します。
// WithHTTPClientを渡すと認証情報の解決が行われないため、鍵なしでAPI呼び出しの形を検証できます。
func newTestDriveClient(t *testing.T, handler http.HandlerFunc) *RealDriveClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	// BasePathの末尾にスラッシュがないと相対パスの結合結果が壊れるため付けておきます。
	svc, err := drive.NewService(context.Background(),
		googleoption.WithHTTPClient(srv.Client()),
		googleoption.WithEndpoint(srv.URL+"/"),
	)
	if err != nil {
		t.Fatalf("drive.NewService() error: %v", err)
	}
	return &RealDriveClient{service: svc}
}

func TestSearchFilesBuildsQuery(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	client := newTestDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[
			{"id":"f1","name":"議事録 2026-08-01","mimeType":"application/vnd.google-apps.document",
			 "webViewLink":"https://docs.google.com/document/d/f1/edit","modifiedTime":"2026-08-01T09:00:00.000Z"}
		]}`))
	})

	files, err := client.SearchFiles(context.Background(), "議事録", driveMimeTypeFor("document"), "folder-1", 5)
	if err != nil {
		t.Fatalf("SearchFiles() error: %v", err)
	}

	if gotPath != "/files" {
		t.Errorf("path = %q, want /files", gotPath)
	}
	q := gotQuery.Get("q")
	// ゴミ箱の中まで拾うと「消したはずの資料」を案内してしまうため必ず除外する。
	for _, want := range []string{
		"trashed = false",
		"name contains '議事録'",
		"fullText contains '議事録'",
		"mimeType = 'application/vnd.google-apps.document'",
		"'folder-1' in parents",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("q = %q, want it to contain %q", q, want)
		}
	}
	if got := gotQuery.Get("pageSize"); got != "5" {
		t.Errorf("pageSize = %q, want 5", got)
	}
	if got := gotQuery.Get("orderBy"); got != "modifiedTime desc" {
		t.Errorf("orderBy = %q, want modifiedTime desc", got)
	}
	// 共有ドライブ上の資料も検索対象にする。
	if got := gotQuery.Get("supportsAllDrives"); got != "true" {
		t.Errorf("supportsAllDrives = %q, want true", got)
	}
	if got := gotQuery.Get("includeItemsFromAllDrives"); got != "true" {
		t.Errorf("includeItemsFromAllDrives = %q, want true", got)
	}

	if len(files) != 1 {
		t.Fatalf("files len = %d, want 1", len(files))
	}
	f := files[0]
	if f.ID != "f1" || f.Name != "議事録 2026-08-01" || f.URL != "https://docs.google.com/document/d/f1/edit" {
		t.Errorf("parsed file mismatch: %+v", f)
	}
}

// アポストロフィを含むキーワードでクエリが壊れないこと(エスケープしないと400になる)。
func TestSearchFilesEscapesQuoteInKeyword(t *testing.T) {
	var gotQuery url.Values
	client := newTestDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"files":[]}`))
	})

	if _, err := client.SearchFiles(context.Background(), "O'Brien", "", "", 0); err != nil {
		t.Fatalf("SearchFiles() error: %v", err)
	}
	if got := gotQuery.Get("q"); !strings.Contains(got, `name contains 'O\'Brien'`) {
		t.Errorf("q = %q, want the single quote escaped", got)
	}
	if got := gotQuery.Get("pageSize"); got != "20" {
		t.Errorf("pageSize = %q, want 20 (limit未指定時の既定値)", got)
	}
}

func TestSearchFilesOmitsEmptyConditions(t *testing.T) {
	var gotQuery url.Values
	client := newTestDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"files":[]}`))
	})

	if _, err := client.SearchFiles(context.Background(), "  ", "", "", 0); err != nil {
		t.Fatalf("SearchFiles() error: %v", err)
	}
	if got := gotQuery.Get("q"); got != "trashed = false" {
		t.Errorf("q = %q, want only the trashed condition", got)
	}
}

func TestListFolderDefaultsToRoot(t *testing.T) {
	var gotQuery url.Values
	client := newTestDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"files":[{"id":"sub","name":"見積書","mimeType":"application/vnd.google-apps.folder"}]}`))
	})

	files, err := client.ListFolder(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("ListFolder() error: %v", err)
	}
	if got := gotQuery.Get("q"); !strings.Contains(got, "'root' in parents") {
		t.Errorf("q = %q, want 'root' in parents (folder_id未指定時)", got)
	}
	if !files[0].IsFolder() {
		t.Errorf("IsFolder() = false, want true for %+v", files[0])
	}
}

// Googleドキュメントはそのままダウンロードできないため、text/plainへエクスポートして読む。
func TestReadFileExportsGoogleDoc(t *testing.T) {
	var exportQuery url.Values
	client := newTestDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/export") {
			exportQuery = r.URL.Query()
			_, _ = w.Write([]byte("本日の議題\n1. 見積の確認"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"doc1","name":"議事録","mimeType":"application/vnd.google-apps.document",
			"webViewLink":"https://docs.google.com/document/d/doc1/edit"}`))
	})

	got, err := client.ReadFile(context.Background(), "doc1", 0)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if want := "text/plain"; exportQuery.Get("mimeType") != want {
		t.Errorf("export mimeType = %q, want %q", exportQuery.Get("mimeType"), want)
	}
	if got.Content != "本日の議題\n1. 見積の確認" {
		t.Errorf("Content = %q", got.Content)
	}
	if got.Truncated {
		t.Error("Truncated = true, want false")
	}
	if got.File.Name != "議事録" {
		t.Errorf("File.Name = %q, want 議事録", got.File.Name)
	}
}

func TestReadFileExportsSpreadsheetAsCSV(t *testing.T) {
	var exportQuery url.Values
	client := newTestDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/export") {
			exportQuery = r.URL.Query()
			_, _ = w.Write([]byte("品目,単価\n工事費,49500"))
			return
		}
		_, _ = w.Write([]byte(`{"id":"s1","name":"見積内訳","mimeType":"application/vnd.google-apps.spreadsheet"}`))
	})

	if _, err := client.ReadFile(context.Background(), "s1", 0); err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if got := exportQuery.Get("mimeType"); got != "text/csv" {
		t.Errorf("export mimeType = %q, want text/csv", got)
	}
}

// テキスト形式はエクスポートせずalt=mediaで直接ダウンロードする。
func TestReadFileDownloadsTextFile(t *testing.T) {
	var downloadAlt string
	client := newTestDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		if alt := r.URL.Query().Get("alt"); alt == "media" {
			downloadAlt = alt
			_, _ = w.Write([]byte("plain text body"))
			return
		}
		_, _ = w.Write([]byte(`{"id":"t1","name":"memo.txt","mimeType":"text/plain; charset=UTF-8"}`))
	})

	got, err := client.ReadFile(context.Background(), "t1", 0)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if downloadAlt != "media" {
		t.Error("alt=media でダウンロードされていない")
	}
	if got.Content != "plain text body" {
		t.Errorf("Content = %q", got.Content)
	}
}

// バイナリを本文として渡しても意味がないため、読まずにエラーにしてリンク案内へ誘導する。
func TestReadFileRejectsBinary(t *testing.T) {
	client := newTestDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") == "media" {
			t.Error("バイナリをダウンロードしようとしている")
		}
		_, _ = w.Write([]byte(`{"id":"p1","name":"契約書.pdf","mimeType":"application/pdf",
			"webViewLink":"https://drive.google.com/file/d/p1/view"}`))
	})

	_, err := client.ReadFile(context.Background(), "p1", 0)
	if err == nil {
		t.Fatal("ReadFile() error = nil, want error")
	}
	// Claudeがそのままユーザーへ案内できるよう、エラー文にURLを含める。
	if !strings.Contains(err.Error(), "https://drive.google.com/file/d/p1/view") {
		t.Errorf("error = %v, want it to contain the file URL", err)
	}
}

func TestReadFileTruncatesLongContent(t *testing.T) {
	client := newTestDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/export") {
			_, _ = w.Write([]byte(strings.Repeat("あ", 30)))
			return
		}
		_, _ = w.Write([]byte(`{"id":"doc1","name":"長文","mimeType":"application/vnd.google-apps.document"}`))
	})

	got, err := client.ReadFile(context.Background(), "doc1", 10)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if !got.Truncated {
		t.Error("Truncated = false, want true")
	}
	// バイト数ではなく文字数で切ること(マルチバイトが途中で割れないこと)。
	if runes := []rune(got.Content); len(runes) != 10 {
		t.Errorf("Content の文字数 = %d, want 10 (%q)", len(runes), got.Content)
	}
	if got.Content != strings.Repeat("あ", 10) {
		t.Errorf("Content = %q", got.Content)
	}
	if text := formatDriveContent(got); !strings.Contains(text, "先頭10文字のみ") {
		t.Errorf("打ち切りの注記がない: %s", text)
	}
}

func TestCreateDocumentUploadsAsGoogleDoc(t *testing.T) {
	var gotPath, gotBody string
	var gotQuery url.Values
	client := newTestDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"id":"new1","name":"議事録 2026-08-13","mimeType":"application/vnd.google-apps.document",
			"webViewLink":"https://docs.google.com/document/d/new1/edit"}`))
	})

	file, err := client.CreateDocument(context.Background(), "議事録 2026-08-13", "folder-1", "本文です")
	if err != nil {
		t.Fatalf("CreateDocument() error: %v", err)
	}

	if gotPath != "/upload/drive/v3/files" {
		t.Errorf("path = %q, want /upload/drive/v3/files", gotPath)
	}
	if got := gotQuery.Get("uploadType"); got == "" {
		t.Error("uploadType が付いていない(メディア付きの作成になっていない)")
	}
	// text/plainをドキュメント形式で投げるとDrive側で変換される。保存先フォルダはparentsで指定する。
	if !strings.Contains(gotBody, "application/vnd.google-apps.document") {
		t.Errorf("body に mimeType がない: %s", gotBody)
	}
	if !strings.Contains(gotBody, "folder-1") {
		t.Errorf("body に parents がない: %s", gotBody)
	}
	if !strings.Contains(gotBody, "本文です") {
		t.Errorf("body に本文がない: %s", gotBody)
	}

	if file.ID != "new1" || file.URL != "https://docs.google.com/document/d/new1/edit" {
		t.Errorf("created file mismatch: %+v", file)
	}
}

func TestCreateDocumentWithoutFolderOmitsParents(t *testing.T) {
	var gotBody string
	client := newTestDriveClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"id":"new2","name":"メモ"}`))
	})

	if _, err := client.CreateDocument(context.Background(), "メモ", "", "本文"); err != nil {
		t.Fatalf("CreateDocument() error: %v", err)
	}
	if strings.Contains(gotBody, "parents") {
		t.Errorf("folder_id未指定なのにparentsが入っている: %s", gotBody)
	}
}

func TestNormalizeDriveLimit(t *testing.T) {
	for in, want := range map[int]int{0: 20, -5: 20, 10: 10, 100: 100, 500: 100} {
		if got := normalizeDriveLimit(in); got != want {
			t.Errorf("normalizeDriveLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestNormalizeDriveMaxChars(t *testing.T) {
	for in, want := range map[int]int{0: 10000, -1: 10000, 500: 500, 50000: 50000, 999999: 50000} {
		if got := normalizeDriveMaxChars(in); got != want {
			t.Errorf("normalizeDriveMaxChars(%d) = %d, want %d", in, got, want)
		}
	}
}

// Claudeに生のMIMEタイプを書かせると誤字で0件になるため、enumの種別名から解決する。
func TestDriveMimeTypeFor(t *testing.T) {
	for in, want := range map[string]string{
		"folder":       "application/vnd.google-apps.folder",
		"document":     "application/vnd.google-apps.document",
		"spreadsheet":  "application/vnd.google-apps.spreadsheet",
		"presentation": "application/vnd.google-apps.presentation",
		"pdf":          "application/pdf",
		"":             "",
		"bogus":        "",
	} {
		if got := driveMimeTypeFor(in); got != want {
			t.Errorf("driveMimeTypeFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsDriveTextMimeType(t *testing.T) {
	for in, want := range map[string]bool{
		"text/plain":                true,
		"text/plain; charset=UTF-8": true,
		"text/markdown":             true,
		"application/json":          true,
		"application/pdf":           false,
		"image/png":                 false,
		"application/vnd.ms-excel":  false,
		"application/octet-stream":  false,
	} {
		if got := isDriveTextMimeType(in); got != want {
			t.Errorf("isDriveTextMimeType(%q) = %v, want %v", in, got, want)
		}
	}
}

// idは後続ツール(drive_read_file等)の入力になるため、一覧に必ず残す。
func TestFormatDriveFilesIncludesIDAndKind(t *testing.T) {
	got := formatDriveFiles("「議事録」の検索結果", []DriveFile{
		{ID: "f1", Name: "議事録", MimeType: "application/vnd.google-apps.document",
			URL: "https://docs.google.com/document/d/f1/edit", ModifiedTime: "2026-08-01T09:00:00.000Z"},
		{ID: "d1", Name: "見積書", MimeType: driveFolderMimeType},
	})

	if !strings.Contains(got, "「議事録」の検索結果: 2件") {
		t.Errorf("件数が出ていない:\n%s", got)
	}
	if !strings.Contains(got, "id: f1") || !strings.Contains(got, "id: d1") {
		t.Errorf("idが出ていない:\n%s", got)
	}
	if !strings.Contains(got, "[ファイル] 議事録") || !strings.Contains(got, "[フォルダ] 見積書") {
		t.Errorf("種別が出ていない:\n%s", got)
	}
}

// ツール定義はapp.New()時に組み立てられるため、名前の変更やスキーマ不整合を起動前に検知する。
func TestNewDriveToolsDefinesExpectedTools(t *testing.T) {
	driveTools, err := NewDriveTools(NewMockDriveClient())
	if err != nil {
		t.Fatalf("NewDriveTools() error: %v", err)
	}

	got := make(map[string]bool, len(driveTools))
	for _, tool := range driveTools {
		got[tool.Name()] = true
	}
	for _, name := range []string{"drive_search_files", "drive_list_folder", "drive_read_file", "drive_create_document"} {
		if !got[name] {
			t.Errorf("ツール %s が定義されていない (定義済み: %v)", name, got)
		}
	}
	// 削除・共有権限変更は誤爆時に戻せないため、意図的に公開しない。
	for _, name := range []string{"drive_delete_file", "drive_share_file"} {
		if got[name] {
			t.Errorf("ツール %s は公開しない方針", name)
		}
	}
	if len(driveTools) != 4 {
		t.Errorf("ツール数 = %d, want 4", len(driveTools))
	}
}

func TestFormatDriveFilesEmpty(t *testing.T) {
	if got := formatDriveFiles("検索結果", nil); !strings.Contains(got, "0件") {
		t.Errorf("0件が伝わらない: %s", got)
	}
}
