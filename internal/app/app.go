package app

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"

	slackapi "github.com/slack-go/slack"

	"github.com/yoshida-rio/marina/internal/agent"
	"github.com/yoshida-rio/marina/internal/bedrockclient"
	"github.com/yoshida-rio/marina/internal/config"
	"github.com/yoshida-rio/marina/internal/mfoauth"
	"github.com/yoshida-rio/marina/internal/morningdigest"
	"github.com/yoshida-rio/marina/internal/proxyreply"
	"github.com/yoshida-rio/marina/internal/slack"
	"github.com/yoshida-rio/marina/internal/storage"
	"github.com/yoshida-rio/marina/internal/tools"
)

// App はSlackハンドラ・Agent・DBなど、marinaの主要コンポーネントを保持します。
type App struct {
	Config              *config.Config
	DB                  *sql.DB
	SlackHandler        *slack.Handler
	InteractionHandler  *slack.InteractionHandler
	MFOAuthHandler      http.Handler
	ReminderRepo        *storage.ReminderRepo
	SlackClient         *slackapi.Client
	MorningDigestRunner *morningdigest.Runner
	Agent               *agent.Agent
	ProxyReply          *proxyreply.Service
}

// New は環境変数から設定を読み込み、DB接続・ツール・Agent・Slackハンドラを構築します。
func New(cfg *config.Config) (*App, error) {
	ctx := context.Background()

	db, err := storage.NewDB(cfg.DBDSN)
	if err != nil {
		return nil, fmt.Errorf("connect db: %w", err)
	}

	taskRepo := storage.NewTaskRepo(db)
	reminderRepo := storage.NewReminderRepo(db)
	conversationRepo := storage.NewConversationRepo(db)

	taskTools, err := tools.NewTaskTools(taskRepo)
	if err != nil {
		return nil, fmt.Errorf("build task tools: %w", err)
	}
	reminderTools, err := tools.NewReminderTools(reminderRepo)
	if err != nil {
		return nil, fmt.Errorf("build reminder tools: %w", err)
	}

	var gmailClient tools.GmailClient = tools.NewMockGmailClient()
	if cfg.HasGmailCredentials() {
		realGmailClient, err := tools.NewRealGmailClient(ctx, cfg.GoogleServiceAccountJSON, cfg.GoogleImpersonatedUser)
		if err != nil {
			return nil, fmt.Errorf("build gmail client: %w", err)
		}
		gmailClient = realGmailClient
	}
	gmailTools, err := tools.NewGmailTools(gmailClient)
	if err != nil {
		return nil, fmt.Errorf("build gmail tools: %w", err)
	}

	var driveClient tools.DriveClient = tools.NewMockDriveClient()
	if cfg.HasDriveCredentials() {
		realDriveClient, err := tools.NewRealDriveClient(ctx, cfg.GoogleServiceAccountJSON, cfg.GoogleImpersonatedUser)
		if err != nil {
			return nil, fmt.Errorf("build drive client: %w", err)
		}
		driveClient = realDriveClient
	}
	driveTools, err := tools.NewDriveTools(driveClient)
	if err != nil {
		return nil, fmt.Errorf("build drive tools: %w", err)
	}

	oauthTokenRepo := storage.NewOAuthTokenRepo(db)
	var mfClient tools.MFInvoiceClient = tools.NewMockMFInvoiceClient()
	var mfOAuthHandler http.Handler
	if cfg.HasMFCredentials() {
		mfOAuthConfig := mfoauth.NewOAuthConfig(cfg.MFClientID, cfg.MFClientSecret, cfg.MFOAuthRedirectURI)
		mfClient = tools.NewRealMFInvoiceClient(mfOAuthConfig, oauthTokenRepo)
		mfOAuthHandler = mfoauth.NewHandler(mfOAuthConfig, oauthTokenRepo)
	}
	mfTools, err := tools.NewMFInvoiceTools(mfClient)
	if err != nil {
		return nil, fmt.Errorf("build mf invoice tools: %w", err)
	}

	allTools := append(append(append(append(taskTools, reminderTools...), gmailTools...), driveTools...), mfTools...)

	anthropicClient, err := bedrockclient.New(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("build bedrock client: %w", err)
	}
	ag := agent.New(anthropicClient, cfg.AnthropicModel, allTools, conversationRepo)

	slackClient := slackapi.New(cfg.SlackBotToken)

	// 代理返信フロー: User OAuthトークンがあり、その持ち主が対象ユーザーと一致する場合のみ有効化する。
	// 有効でない場合は slack.ProxyReplier をnilのまま渡し、従来どおりmarina自身が応答する。
	var proxyService *proxyreply.Service
	var proxyReplier slack.ProxyReplier
	if targetUserID, userClient := resolveProxyReplyTarget(cfg); userClient != nil {
		proxyService = proxyreply.NewService(
			proxyreply.Config{
				TargetUserID:    targetUserID,
				AllowedChannels: cfg.ProxyReplyChannelIDs,
				IncludeDM:       cfg.ProxyReplyIncludeDM,
			},
			ag,
			storage.NewReplyDraftRepo(db),
			slackClient,
			userClient,
		)
		proxyReplier = proxyService
	}

	handler := slack.NewHandler(cfg.SlackBotToken, cfg.SlackSigningSecret, cfg.SlackTypingEmoji, ag, proxyReplier)
	interactionHandler := slack.NewInteractionHandler(cfg.SlackSigningSecret, proxyReplier)
	digestRunner := morningdigest.NewRunner(anthropicClient, cfg.AnthropicModel, gmailClient, slackClient, cfg.MorningDigestSlackChannel)

	return &App{
		Config:              cfg,
		DB:                  db,
		SlackHandler:        handler,
		InteractionHandler:  interactionHandler,
		MFOAuthHandler:      mfOAuthHandler,
		ReminderRepo:        reminderRepo,
		SlackClient:         slackClient,
		MorningDigestRunner: digestRunner,
		Agent:               ag,
		ProxyReply:          proxyService,
	}, nil
}

// HandleAsyncRequest は受信Lambdaから非同期invokeされたSlackリクエストを同期的に処理します。
// ワーカーLambda(cmd/eventworker)から呼ばれます。
func (a *App) HandleAsyncRequest(req slack.AsyncRequest) error {
	switch req.Kind {
	case slack.AsyncKindEvents:
		return a.SlackHandler.HandleEventPayload([]byte(req.Body))
	case slack.AsyncKindInteractions:
		return a.InteractionHandler.HandleInteractionPayload([]byte(req.Body))
	default:
		return fmt.Errorf("unknown async request kind %q", req.Kind)
	}
}

// resolveProxyReplyTarget は代理返信フローの対象ユーザーIDとUser OAuthトークンのクライアントを返します。
// 代理返信できるのはトークンの持ち主本人だけなので、auth.testで実際の持ち主を確認し、
// PROXY_REPLY_TARGET_USER_IDと食い違う場合は誤った相手として投稿しないよう機能を無効化します。
// PROXY_REPLY_TARGET_USER_IDが未設定の場合はトークンの持ち主を対象ユーザーとして採用します。
// 無効な場合は (\"\", nil) を返します。
func resolveProxyReplyTarget(cfg *config.Config) (string, *slackapi.Client) {
	if cfg.SlackUserOAuthToken == "" {
		return "", nil
	}

	userClient := slackapi.New(cfg.SlackUserOAuthToken)
	auth, err := userClient.AuthTest()
	if err != nil {
		log.Printf("proxy reply disabled: user oauth token auth test failed: %v", err)
		return "", nil
	}

	switch {
	case cfg.ProxyReplyTargetUserID == "":
		log.Printf("proxy reply target user resolved from user oauth token: %s", auth.UserID)
	case cfg.ProxyReplyTargetUserID != auth.UserID:
		log.Printf("proxy reply disabled: PROXY_REPLY_TARGET_USER_ID=%s does not match the user oauth token owner (%s). "+
			"SLACK_USER_OAUTH_TOKEN must be authorized by the target user themselves",
			cfg.ProxyReplyTargetUserID, auth.UserID)
		return "", nil
	}
	return auth.UserID, userClient
}

// Mux はSlackイベント・ヘルスチェック・(設定されていれば)MoneyForward OAuthコールバックを
// まとめて登録したhttp.ServeMuxを構築します。
func (a *App) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/slack/events", a.SlackHandler)
	mux.Handle("/slack/interactions", a.InteractionHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	if a.MFOAuthHandler != nil {
		mux.Handle("/mf/oauth/", a.MFOAuthHandler)
	}
	return mux
}
