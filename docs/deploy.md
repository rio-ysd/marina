# デプロイ (AWS CDK + GitHub Actions)

`main`へのpushで GitHub Actions が CDK デプロイとマイグレーションを実行します。
インフラ定義は [infra/](../infra/) (AWS CDK / TypeScript)、ワークフローは
[.github/workflows/](../.github/workflows/) です。

## 構成

```
Slack ──> API Gateway (HTTP API) ──> Lambda: SlackReceiver (署名検証 + ack)
                                        │ 非同期invoke
                                        ▼
                                     Lambda: EventWorker (Claude呼び出し / Slackへ投稿)
                                        │
EventBridge (平日9:00 JST) ─────────> Lambda: MorningDigest
EventBridge (5分おき) ──────────────> Lambda: ReminderWorker
                                        │
                                        ▼
                                     RDS MySQL 8.0 (t4g.micro, パブリック公開)
                                     Secrets Manager (marina/app, marina/db)
```

- リージョン: `ap-northeast-1`(Bedrockのみ `us-east-1` を利用)
- Lambda: `provided.al2023` / arm64。`scripts/build-lambdas.sh` が `dist/<name>/bootstrap` を作り、CDKがそれをアセットとして参照します
- 秘密情報はLambdaの環境変数に置かず、起動時に Secrets Manager から読み込みます([internal/config/secrets.go](../internal/config/secrets.go))

### なぜ受信とワーカーを分けているか

Lambdaはハンドラが return すると実行環境が凍結されるため、「Slackへ3秒以内にackしてから
goroutineで処理を続ける」方式が使えません(ローカルの`cmd/server`ではこれで動きます)。
そのため受信Lambdaは署名検証とackのみを行い、実処理はワーカーLambdaを非同期invokeして任せます。

### ネットワーク構成の注意

LambdaはVPC外に置き、RDSを `publiclyAccessible` にしています。NAT Gateway / NATインスタンスの
コスト($5〜45/月)を避け、GitHub Actionsから直接マイグレーションを流せるようにするためです。
代わりにRDSのセキュリティグループは `0.0.0.0/0:3306` を許可しています
(VPC外Lambdaの送信元IPが固定できないため)。以下で保護しています。

- Secrets Managerが生成する32文字のパスワード
- `require_secure_transport=ON` によりTLSなしの接続を拒否
- 保管時の暗号化(`storageEncrypted`)、7日間の自動バックアップ、削除保護

**未対応(改善候補)**: アプリからの接続は `tls=skip-verify` で、通信は暗号化されますが
サーバー証明書の検証はしていません(Amazon RDSのCAがシステムの信頼ストアに無いため)。
検証まで行うにはRDSのCA証明書をバイナリに同梱する対応が必要です。

## 初回セットアップ(手動、1回だけ)

### 1. OIDCロールを作成する

GitHub ActionsがAWSを操作するためのロールを、手元の管理者権限でデプロイします
(このロールが無い状態ではGitHub Actionsから作れないため)。

```sh
cd infra
npm ci
npm run deploy -- MarinaGithubOidcStack
```

出力される `DeployRoleArn` を、GitHubリポジトリの
**Settings → Secrets and variables → Actions → New repository secret** に登録します。

| Secret名 | 値 |
|---|---|
| `AWS_DEPLOY_ROLE_ARN` | `arn:aws:iam::<account>:role/marina-github-actions-deploy` |

### 2. 本体をデプロイする

`main`にpushすれば GitHub Actions が実行します。手元から流す場合:

```sh
./scripts/build-lambdas.sh
cd infra && npm run deploy -- MarinaStack
```

### 3. アプリ設定(Slackトークン等)を投入する

`marina/app` シークレットは空の値で作られるので、実際の値を入れます。
Secrets Managerのコンソールでも、CLIでも構いません。

```sh
aws secretsmanager put-secret-value --secret-id marina/app --region ap-northeast-1 \
  --secret-string "$(jq -n \
    --arg bot "$SLACK_BOT_TOKEN" \
    --arg signing "$SLACK_SIGNING_SECRET" \
    --arg user "$SLACK_USER_OAUTH_TOKEN" \
    --arg target "$PROXY_REPLY_TARGET_USER_ID" \
    --arg digest "$MORNING_DIGEST_SLACK_CHANNEL" \
    '{SLACK_BOT_TOKEN:$bot, SLACK_SIGNING_SECRET:$signing, SLACK_USER_OAUTH_TOKEN:$user,
      PROXY_REPLY_TARGET_USER_ID:$target, PROXY_REPLY_INCLUDE_DM:"true",
      MORNING_DIGEST_SLACK_CHANNEL:$digest}')"
```

Gmail連携・MoneyForward連携を使う場合は `GOOGLE_SERVICE_ACCOUNT_JSON` /
`GOOGLE_IMPERSONATED_USER` / `MF_CLIENT_ID` / `MF_CLIENT_SECRET` / `MF_OAUTH_REDIRECT_URI` も
同じシークレットに入れます(未設定ならモック実装で動作します)。

### 4. Slack AppのURLを本番に向ける

デプロイのサマリー(またはスタック出力 `ApiEndpoint`)のURLを設定します。

| Slack Appの設定 | URL |
|---|---|
| Event Subscriptions → Request URL | `<ApiEndpoint>/slack/events` |
| Interactivity → Request URL | `<ApiEndpoint>/slack/interactions` |
| MoneyForward OAuth (`MF_OAUTH_REDIRECT_URI`) | `<ApiEndpoint>/mf/oauth/callback` |

**Socket Modeを有効にしていると本番のRequest URLにイベントが届きません。**
本番運用時はSlack AppのSocket ModeをOFFにしてください
(ローカル検証用の`cmd/socketmode`はLambdaでは動きません)。

## 日々のデプロイ

1. ブランチを切ってPRを出す → CI(`go vet` / `go test` / `gofmt` / `cdk synth`)が回る
2. `main`にマージ → Deployワークフローが `cdk deploy` とマイグレーションを実行

マイグレーションはデプロイの**後**に実行されます。カラム削除など後方互換のない変更は、
一時的に新旧どちらのコードでも動く状態を経由させてください。

## 費用の目安(月額、ap-northeast-1)

| リソース | 概算 |
|---|---|
| RDS db.t4g.micro (Single-AZ, gp3 20GB) | ~$17 |
| Secrets Manager (2シークレット) | ~$1 |
| Lambda / API Gateway / EventBridge / CloudWatch Logs | ~$0(無料枠内) |
| Bedrock (Claude) | 利用量課金 |
| **合計** | **~$18 + Bedrock利用料** |

NAT Gateway($45/月)もNATインスタンス($5/月)も使わない構成のため、固定費はRDSがほぼ全てです。

## トラブルシューティング

| 症状 | 確認 |
|---|---|
| Slackが「操作に失敗しました」と表示する | `SlackReceiver`のログ。署名検証失敗なら`SLACK_SIGNING_SECRET`がシークレットの値と一致しているか |
| ackは通るが返信が来ない | `EventWorker`のログ。Bedrockの権限・モデルアクセス、DB接続を確認 |
| `load secrets` エラーで起動失敗 | `marina/app`が有効なJSONオブジェクトか、Lambdaロールに`secretsmanager:GetSecretValue`があるか |
| マイグレーションがタイムアウトする | RDSのセキュリティグループが`0.0.0.0/0:3306`を許可しているか(GitHub Actionsのランナーから接続します) |
| デプロイが `deletionProtection` で失敗する | RDSは削除保護有効。破棄する場合はコンソールで保護を外してから`cdk destroy` |
