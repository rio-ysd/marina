#!/usr/bin/env bash
# ローカルでmysqlを起動し、朝のGmailダイジェスト処理
# (不要メールの既読化・要確認メールの抽出・返信下書き作成・Slack通知)を一度だけ実行するスクリプト。
set -euo pipefail

cd "$(dirname "$0")/.."

if [ ! -f .env ]; then
  echo ".env が見つかりません。.env.example を参考に作成してください。" >&2
  exit 1
fi

docker compose --env-file .env up -d mysql

echo "mysqlの起動を待機中..."
until docker compose --env-file .env exec -T mysql mysqladmin ping -h localhost -u root -pmarina --silent < /dev/null >/dev/null 2>&1; do
  sleep 1
done

echo "マイグレーションを適用中..."
docker compose --env-file .env up migrate < /dev/null

set -a
source .env
set +a
export DB_DSN="marina:marina@tcp(127.0.0.1:3306)/marina?parseTime=true&loc=Local"

go run ./cmd/morningdigest-local
