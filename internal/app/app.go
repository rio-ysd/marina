package app

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"

	anthropic "github.com/anthropics/anthropic-sdk-go"
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
	"github.com/yoshida-rio/marina/internal/userprovision"
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

	// アカウント作成の承認DMを送るためにツール構築より先にSlackクライアントを用意する。
	slackClient := slackapi.New(cfg.SlackBotToken)

	// Google連携(Gmail / Drive / カレンダー / スプレッドシート / People / Admin SDK)は
	// 同じサービスアカウントを使うため、認証情報の有無をまとめて判定する。
	// 未設定ならいずれもモッククライアントで動作する。
	hasGoogle := cfg.HasGoogleCredentials()

	var gmailClient tools.GmailClient = tools.NewMockGmailClient()
	if hasGoogle {
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
	if hasGoogle {
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

	var calendarClient tools.CalendarClient = tools.NewMockCalendarClient()
	if hasGoogle {
		realCalendarClient, err := tools.NewRealCalendarClient(ctx, cfg.GoogleServiceAccountJSON, cfg.GoogleImpersonatedUser)
		if err != nil {
			return nil, fmt.Errorf("build calendar client: %w", err)
		}
		calendarClient = realCalendarClient
	}
	calendarTools, err := tools.NewCalendarTools(calendarClient)
	if err != nil {
		return nil, fmt.Errorf("build calendar tools: %w", err)
	}

	var sheetsClient tools.SheetsClient = tools.NewMockSheetsClient()
	if hasGoogle {
		realSheetsClient, err := tools.NewRealSheetsClient(ctx, cfg.GoogleServiceAccountJSON, cfg.GoogleImpersonatedUser)
		if err != nil {
			return nil, fmt.Errorf("build sheets client: %w", err)
		}
		sheetsClient = realSheetsClient
	}
	sheetsTools, err := tools.NewSheetsTools(sheetsClient)
	if err != nil {
		return nil, fmt.Errorf("build sheets tools: %w", err)
	}

	var peopleClient tools.PeopleClient = tools.NewMockPeopleClient()
	if hasGoogle {
		realPeopleClient, err := tools.NewRealPeopleClient(ctx, cfg.GoogleServiceAccountJSON, cfg.GoogleImpersonatedUser)
		if err != nil {
			return nil, fmt.Errorf("build people client: %w", err)
		}
		peopleClient = realPeopleClient
	}
	peopleTools, err := tools.NewPeopleTools(peopleClient)
	if err != nil {
		return nil, fmt.Errorf("build people tools: %w", err)
	}

	// アカウント作成フロー: 承認者(USER_PROVISION_APPROVER_SLACK_USER_ID)が設定されている場合のみ
	// ツールを登録する。未設定ならツール自体を渡さないので、Claudeは作成を試みることすらできない。
	var directoryClient tools.DirectoryClient = tools.NewMockDirectoryClient()
	if hasGoogle {
		realDirectoryClient, err := tools.NewRealDirectoryClient(ctx, cfg.GoogleServiceAccountJSON, cfg.GoogleImpersonatedUser)
		if err != nil {
			return nil, fmt.Errorf("build directory client: %w", err)
		}
		directoryClient = realDirectoryClient
	}
	userProvision := userprovision.NewService(
		cfg.UserProvisionApproverUserID,
		storage.NewUserCreationRequestRepo(db),
		directoryClient,
		slackClient,
	)
	var directoryTools []anthropic.BetaTool
	var userCreationApprover slack.UserCreationApprover
	if userProvision != nil {
		directoryTools, err = tools.NewDirectoryTools(directoryClient, userProvision)
		if err != nil {
			return nil, fmt.Errorf("build directory tools: %w", err)
		}
		userCreationApprover = userProvision
		log.Printf("user provisioning enabled: approver=%s", userProvision.ApproverUserID())
	} else {
		log.Println("user provisioning disabled: USER_PROVISION_APPROVER_SLACK_USER_ID is not set")
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

	// ツールの並び順はClaudeへの提示順。連携が増えたら追加するだけで済むようにまとめて連結する。
	var allTools []anthropic.BetaTool
	for _, set := range [][]anthropic.BetaTool{
		taskTools, reminderTools, gmailTools, driveTools, calendarTools, sheetsTools,
		peopleTools, directoryTools, mfTools,
	} {
		allTools = append(allTools, set...)
	}

	anthropicClient, err := bedrockclient.New(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("build bedrock client: %w", err)
	}
	ag := agent.New(anthropicClient, cfg.AnthropicModel, allTools, conversationRepo)

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
	interactionHandler := slack.NewInteractionHandler(cfg.SlackSigningSecret, proxyReplier, userCreationApprover)
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
