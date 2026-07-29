package app

import (
	"context"
	"database/sql"
	"fmt"

	slackapi "github.com/slack-go/slack"

	"github.com/yoshida-rio/marina/internal/agent"
	"github.com/yoshida-rio/marina/internal/bedrockclient"
	"github.com/yoshida-rio/marina/internal/config"
	"github.com/yoshida-rio/marina/internal/morningdigest"
	"github.com/yoshida-rio/marina/internal/slack"
	"github.com/yoshida-rio/marina/internal/storage"
	"github.com/yoshida-rio/marina/internal/tools"
)

// App はSlackハンドラ・Agent・DBなど、marinaの主要コンポーネントを保持します。
type App struct {
	Config              *config.Config
	DB                  *sql.DB
	SlackHandler        *slack.Handler
	ReminderRepo        *storage.ReminderRepo
	SlackClient         *slackapi.Client
	MorningDigestRunner *morningdigest.Runner
	Agent               *agent.Agent
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

	var mfClient tools.MFInvoiceClient = tools.NewMockMFInvoiceClient()
	if cfg.HasMFCredentials() {
		// TODO: MoneyForwardクラウド会計のOAuthクライアントに切り替える。
	}
	mfTools, err := tools.NewMFInvoiceTools(mfClient)
	if err != nil {
		return nil, fmt.Errorf("build mf invoice tools: %w", err)
	}

	allTools := append(append(append(taskTools, reminderTools...), gmailTools...), mfTools...)

	anthropicClient, err := bedrockclient.New(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("build bedrock client: %w", err)
	}
	ag := agent.New(anthropicClient, cfg.AnthropicModel, allTools, conversationRepo)

	handler := slack.NewHandler(cfg.SlackBotToken, cfg.SlackSigningSecret, ag)
	slackClient := slackapi.New(cfg.SlackBotToken)
	digestRunner := morningdigest.NewRunner(anthropicClient, cfg.AnthropicModel, gmailClient, slackClient, cfg.MorningDigestSlackChannel)

	return &App{
		Config:              cfg,
		DB:                  db,
		SlackHandler:        handler,
		ReminderRepo:        reminderRepo,
		SlackClient:         slackClient,
		MorningDigestRunner: digestRunner,
		Agent:               ag,
	}, nil
}
