# Googleスプレッドシート連携

スプレッドシートの構造確認・範囲の読み取り・末尾への追記をFunction Callingツールとして提供する。
実装方針の背景は[docs/integration-guidelines.md](integration-guidelines.md)を参照。

## 認証方式

Gmail / Drive / カレンダーと同じGoogle Workspaceサービスアカウント + ドメイン委任を使う。
環境変数は共通(`GOOGLE_SERVICE_ACCOUNT_JSON` / `GOOGLE_IMPERSONATED_USER`)で、
スプレッドシート用に増えるものはない。セットアップ手順は[docs/google-drive.md](google-drive.md)と同じ。

必要なスコープ:

```
https://www.googleapis.com/auth/spreadsheets
```

行を追記するため、参照専用の `spreadsheets.readonly` では足りない。

## Drive連携との使い分け

`drive_read_file` でもスプレッドシートはCSVとして読めるが、シート全体が対象になる。
シートを選んで範囲で読む・追記するにはこちらを使う。

| やりたいこと | 使うツール |
|---|---|
| スプレッドシートを探す | `drive_search_files` (`file_type=spreadsheet`) |
| ざっと中身を見る | `drive_read_file` |
| シートを選んで範囲で読む | `sheets_read_range` |
| 行を追記する | `sheets_append_rows` |

## 提供するツール

| ツール | 用途 | 対応するAPI |
|---|---|---|
| `sheets_get_info` | タイトルとシート(タブ)の一覧。範囲指定の前提を揃える | `GET /spreadsheets/{id}` |
| `sheets_read_range` | A1記法の範囲を読み取る(例: `売上!A1:D20`) | `GET /spreadsheets/{id}/values/{range}` |
| `sheets_append_rows` | シート末尾に行を追記する | `POST /spreadsheets/{id}/values/{range}:append` |

典型的な流れは `drive_search_files` → `sheets_get_info` → `sheets_read_range`(見出し確認) →
`sheets_append_rows`。ツール反復上限は8回なので、この4段でも収まる。

### 読み取りの制限

- 既定50行・最大500行。超過分は打ち切り、打ち切ったことを結果に明記する
- 1セルは200文字で打ち切る(長文メモの列で入力が膨らむのを防ぐ)。文字数はrune単位
- セルは表示上の値(FORMATTED_VALUE)で取得する。数値は文字列化し、整数に `.0` を付けない
- `fields` を絞って取得する(絞らないと全セルのデータまで返る)

### 追記の挙動

- `valueInputOption=USER_ENTERED` — 入力欄に打ち込んだのと同じ扱い。日付や数値として解釈され、数式も有効になる
- `insertDataOption=INSERT_ROWS` — 行を挿入して追記する。`OVERWRITE` は既存行を壊すため使わない

## 実装しなかったもの

- **既存セルの上書き(`values.update`)** — 失ったデータに気づけないまま進んでしまう。
  追記だけなら元の行は残るので復旧できる
- **範囲のクリア、シートの追加・削除、書式変更** — 同じ理由
- **新規スプレッドシートの作成** — 必要になったら追加する(現状は既存シートへの追記のみ)

上書きが必要になった場合は、[代理返信フロー](proxy-reply.md)と同じYes/No確認を挟む形を検討する。

## ファイル

- [internal/tools/gsheets.go](../internal/tools/gsheets.go) — 型、`SheetsClient`、モック、ツール定義
- [internal/tools/gsheets_real.go](../internal/tools/gsheets_real.go) — `RealSheetsClient`(Sheets API v4)
- [internal/tools/gsheets_test.go](../internal/tools/gsheets_test.go) — httptestでの検証
