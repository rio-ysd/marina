# Slack APIでのメッセージ手動送信手順

Slack MCPサーバー等を経由せず、`curl`から直接Slack Web API (`chat.postMessage`) を呼び出して、任意のチャンネルにメッセージを送信する手順。

## 前提

- `.env` にOAuthトークンが記載されていること（このファイルはリポジトリにコミットしない）

```
SLACK_USER_OAUTH_TOKEN=xoxp-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

- `SLACK_USER_OAUTH_TOKEN`（`xoxp-`）に `chat:write` スコープが付与されていること
  - このトークンを使うと、トークンを発行したユーザー自身の発言としてメッセージが投稿される

## 手順

### 1. トークンを取得する

`.env` を読み込み、環境変数からトークンを取得する。

```bash
set -a
source .env
set +a

TOKEN="${SLACK_USER_OAUTH_TOKEN}"
```

### 2. メッセージ本文をファイルに書き出す

日本語（マルチバイト文字）を含むJSONは `-d` のシェル展開経由で渡すと壊れることがあるため、一時ファイル経由で `--data-binary @file` を使う。

```bash
printf '{"channel":"<チャンネルID>","text":"<メッセージ本文>"}' > /tmp/slack_payload.json
```

### 3. APIを呼び出す

`curl` のHTTP/2実装で `chat.postMessage` へのPOSTが失敗する（`curl: (43)` やレスポンス0バイト）ことがあるため、`--http1.1` を明示する。

```bash
curl -s --http1.1 -o /tmp/slack_resp.json -w "%{http_code}\n" https://slack.com/api/chat.postMessage \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json; charset=utf-8" \
  --data-binary @/tmp/slack_payload.json

cat /tmp/slack_resp.json
```

- HTTPステータス `200` かつレスポンスJSONの `"ok":true` で送信成功
- `"ok":false` の場合は `"error"` フィールドにSlack側のエラーコード（例: `invalid_auth`, `channel_not_found`, `missing_scope`）が入る

### 4. 後始末

トークンを含む一時ファイルを削除する。

```bash
rm -f /tmp/slack_payload.json /tmp/slack_resp.json
```

## トラブルシューティング

| 症状 | 原因 | 対処 |
|---|---|---|
| `curl: (43) A libcurl function was given a bad argument` / レスポンスが空 | HTTP/2でのリクエストが不安定 | `--http1.1` を付ける |
| レスポンスボディが `Bad Request`（JSON以外） | Authorizationヘッダーのトークンが不正な値になっている | `echo "${#TOKEN}"` で長さを確認し、`.env` の値が正しく読み込まれているか確認する |
| `"ok":false, "error":"missing_scope"` | トークンに必要なスコープ（`chat:write`）が付与されていない | Slack Appの管理画面でスコープを追加し、トークンを再発行（再インストール）する |
| `"ok":false, "error":"channel_not_found"` | Botまたはユーザーがチャンネルに参加していない、チャンネルIDが誤っている | チャンネルに参加させる、IDを再確認する |

## 注意事項

- `.env` にはOAuthトークンという機密情報が含まれるため、`.gitignore` 対象にし、絶対にコミットしない
- コマンド実行時にトークンを `echo` や `cat -A` などでそのまま標準出力に出さない（ログ・履歴に残るため）
- 本手順はあくまで手動デバッグ・検証用。恒常的な運用にはSlack MCPサーバー等、専用の統合を利用すること
