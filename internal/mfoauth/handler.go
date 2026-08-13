package mfoauth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/yoshida-rio/marina/internal/storage"
)

const (
	provider     = "moneyforward"
	account      = "default"
	stateTTL     = 10 * time.Minute
	startPath    = "/mf/oauth/start"
	callbackPath = "/mf/oauth/callback"
)

// NewOAuthConfig はMoneyForwardクラウド請求書API用のOAuth2設定を構築します。
func NewOAuthConfig(clientID, clientSecret, redirectURI string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURI,
		Scopes:       []string{"mfc/invoice/data.write"},
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://api.biz.moneyforward.com/authorize",
			TokenURL: "https://api.biz.moneyforward.com/token",
			// アプリポータルの登録をCLIENT_SECRET_POSTにしているため、client_id/client_secretを
			// リクエストボディで送る。既定のAutoDetectでもBasic認証で1回失敗してから
			// POSTへフォールバックして動くが、無駄な往復を避けるため明示する。
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

// Handler はMoneyForwardのOAuth2認可コールバックを処理するHTTPハンドラです。
// 初回のみ人間がブラウザで/mf/oauth/startにアクセスして認可し、以降はrefresh_tokenで自動更新されます。
type Handler struct {
	oauthConfig *oauth2.Config
	tokenRepo   *storage.OAuthTokenRepo

	mu     sync.Mutex
	states map[string]time.Time
}

// NewHandler はHandlerを構築します。
func NewHandler(oauthConfig *oauth2.Config, tokenRepo *storage.OAuthTokenRepo) *Handler {
	return &Handler{
		oauthConfig: oauthConfig,
		tokenRepo:   tokenRepo,
		states:      make(map[string]time.Time),
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case startPath:
		h.handleStart(w, r)
	case callbackPath:
		h.handleCallback(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) handleStart(w http.ResponseWriter, r *http.Request) {
	state, err := generateState()
	if err != nil {
		http.Error(w, "failed to generate state", http.StatusInternalServerError)
		return
	}
	h.saveState(state)

	url := h.oauthConfig.AuthCodeURL(state, oauth2.AccessTypeOffline)
	http.Redirect(w, r, url, http.StatusFound)
}

func (h *Handler) handleCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	state := query.Get("state")
	code := query.Get("code")

	if state == "" || !h.consumeState(state) {
		http.Error(w, "invalid or expired state", http.StatusBadRequest)
		return
	}
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	token, err := h.oauthConfig.Exchange(ctx, code)
	if err != nil {
		log.Printf("mfoauth: exchange failed: %v", err)
		http.Error(w, "failed to exchange authorization code", http.StatusBadGateway)
		return
	}

	var expiresAt *time.Time
	if !token.Expiry.IsZero() {
		expiresAt = &token.Expiry
	}
	if err := h.tokenRepo.Upsert(ctx, provider, account, token.AccessToken, token.RefreshToken, expiresAt); err != nil {
		log.Printf("mfoauth: failed to save token: %v", err)
		http.Error(w, "failed to save token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprint(w, "MoneyForwardの認可が完了しました。このタブは閉じて構いません。")
}

func (h *Handler) saveState(state string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.states[state] = time.Now().Add(stateTTL)
	for s, exp := range h.states {
		if time.Now().After(exp) {
			delete(h.states, s)
		}
	}
}

func (h *Handler) consumeState(state string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	exp, ok := h.states[state]
	if !ok {
		return false
	}
	delete(h.states, state)
	return time.Now().Before(exp)
}

func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
