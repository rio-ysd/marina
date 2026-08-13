# Google Drive連携

Google Driveの資料検索・読み取り・ドキュメント作成をFunction Callingツールとして提供する。
実装方針の背景は[docs/integration-guidelines.md](integration-guidelines.md)を参照。

## 認証方式

**Gmail連携と同じGoogle Workspaceサービスアカウント + ドメイン委任**を使う。
環境変数も共通で、Drive用に新しい変数は増えていない。

| 環境変数 | 内容 |
|---|---|
| `GOOGLE_SERVICE_ACCOUNT_JSON` | サービスアカウントの秘密鍵JSON文字列(Gmailと共通) |
| `GOOGLE_IMPERSONATED_USER` | ドメイン委任で代理するユーザーのメールアドレス(Gmailと共通) |

いずれかが未設定の場合は`MockDriveClient`で動作する(起動は成功し、モック応答が返る)。

### 必要なスコープ

```
https://www.googleapis.com/auth/drive
```

`drive.file`(アプリが作成したファイルのみ)では、**既存フォルダを保存先に指定した作成ができない**
(親フォルダが404になる)ため、フルスコープを使っている。代わりに削除・共有権限変更のツールは
公開しておらず、操作範囲はツールセットで限定している。

### セットアップ(ユーザー作業)

1. Google Cloudコンソールで、Gmail連携で使っているプロジェクトの**Google Drive API**を有効化する
2. Google Workspace管理コンソール → セキュリティ → アクセスとデータ管理 → **APIの制御** →
   ドメイン全体の委任 を開く
3. Gmail用に登録済みのサービスアカウント(クライアントID)の設定を編集し、**OAuthスコープに
   `https://www.googleapis.com/auth/drive` を追加**する
4. `.env`の`GOOGLE_SERVICE_ACCOUNT_JSON` / `GOOGLE_IMPERSONATED_USER`は設定済みならそのまま
5. Slackで「〇〇の資料探して」と依頼して動作確認する

スコープの追加を忘れると、起動と初期化は成功したうえでツール呼び出し時に
`unauthorized_client`(または`insufficient permission`)で失敗する。ログにエラーが出るので、
ツールが空振りする場合はまずここを確認する。

marinaが見られるのは`GOOGLE_IMPERSONATED_USER`のユーザーがアクセスできる範囲。
共有ドライブ上のファイルも対象になるよう`supportsAllDrives` / `includeItemsFromAllDrives`を有効にしている。

## 提供するツール

| ツール | 用途 | 対応するAPI |
|---|---|---|
| `drive_search_files` | ファイル名 + 本文(全文検索)でのキーワード検索。`file_type`で種別、`folder_id`でフォルダを絞れる | `GET /files` (`q`) |
| `drive_list_folder` | 指定フォルダ直下の一覧(省略時はマイドライブ直下) | `GET /files` (`'<id>' in parents`) |
| `drive_read_file` | ファイル本文をテキストで取得 | `GET /files/{id}/export` または `GET /files/{id}?alt=media` |
| `drive_create_document` | テキスト内容のGoogleドキュメントを作成 | `POST /upload/drive/v3/files` |

- 検索結果はゴミ箱を除外し(`trashed = false`)、更新日時の降順で返す
- 一覧は既定20件・最大100件。結果には後続ツールに渡す`id`を必ず含める
- 本文は既定10000文字・最大50000文字で、超過分は打ち切って「打ち切った」ことを明記する
  (文字数はrune単位なのでマルチバイトが割れない)

### 読み取れる形式

| 形式 | 挙動 |
|---|---|
| Googleドキュメント | `text/plain`へエクスポート |
| スプレッドシート | `text/csv`へエクスポート |
| スライド | `text/plain`へエクスポート |
| `text/*`、JSON/XML/YAML等 | そのままダウンロード |
| PDF・画像・Officeファイル | **読み取らずエラー**。エラー文にファイル名とURLを含め、Claudeがリンク案内に切り替える |

PDFの本文読み取りは未対応。必要になったらテキスト抽出の手段(Drive側の変換 or ライブラリ)を
決めてから`download`に分岐を足す。

## 実装しなかったもの

- **削除・ゴミ箱への移動** — Slackの曖昧な指示で誤爆すると戻せない
- **共有権限の変更** — 意図せず外部公開になる事故が起きうる
- **既存ファイルの上書き更新** — 履歴は残るが、内容を失った状態でSlackに気づかないまま進む
- **PDFの本文抽出** — 上記のとおり未対応

## ファイル

- [internal/tools/gdrive.go](../internal/tools/gdrive.go) — 型、`DriveClient`、`MockDriveClient`、ツール定義
- [internal/tools/gdrive_real.go](../internal/tools/gdrive_real.go) — `RealDriveClient`(Drive API v3)
- [internal/tools/gdrive_test.go](../internal/tools/gdrive_test.go) — httptestでのリクエスト組み立て/パース検証
- [internal/config/config.go](../internal/config/config.go) — `HasDriveCredentials()`
- [internal/app/app.go](../internal/app/app.go) — クライアント切り替えとツールの配線
