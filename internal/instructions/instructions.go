// Package instructions は「ユーザーからの追加指示」を保持します。
// Slack App Homeのタブから編集され、Slack会話の応答と朝のメールダイジェストの
// システムプロンプトに差し込まれます。コードを変更せずに運用ルールを足すための仕組みです。
package instructions

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/yoshida-rio/marina/internal/storage"
)

// SettingName はapp_settingsテーブルでの保存キーです。
const SettingName = "custom_instructions"

// MaxLen は保存できる指示の最大文字数(rune)です。
// システムプロンプトは全リクエストに乗るため、際限なく伸ばせないように上限を設けています。
// Slackのplain_text_inputのmax_lengthとも揃えてあります。
const MaxLen = 3000

// promptHeading はプロンプトへ差し込むときの見出しです。
// 本来の指示と混ざらないよう、必ずこの見出しの下に置きます。
const promptHeading = "# ユーザーが設定した追加の運用ルール"

// Store は追加指示の読み書きを提供します。
type Store struct {
	repo *storage.SettingRepo
}

func NewStore(repo *storage.SettingRepo) *Store {
	return &Store{repo: repo}
}

// Get は保存されている指示を更新者・更新日時ごと返します。App Homeの表示に使います。
func (s *Store) Get(ctx context.Context) (storage.Setting, error) {
	if s == nil || s.repo == nil {
		return storage.Setting{Name: SettingName}, nil
	}
	return s.repo.Get(ctx, SettingName)
}

// Text はプロンプトへ差し込む指示本文を返します。
// 追加指示はあくまで補助なので、取得に失敗した場合はログだけ残して空文字を返し、
// 本来の応答やダイジェストは止めません。
func (s *Store) Text(ctx context.Context) string {
	setting, err := s.Get(ctx)
	if err != nil {
		log.Printf("instructions: load failed (ignored): %v", err)
		return ""
	}
	return setting.Value
}

// Save は指示を保存します。前後の空白を落とし、上限を超える場合はエラーにします。
func (s *Store) Save(ctx context.Context, text, updatedBy string) error {
	text = strings.TrimSpace(text)
	if len([]rune(text)) > MaxLen {
		return fmt.Errorf("追加指示は%d文字までです", MaxLen)
	}
	if s == nil || s.repo == nil {
		return fmt.Errorf("instructions store is not configured")
	}
	return s.repo.Set(ctx, SettingName, text, updatedBy)
}

// PromptSection はシステムプロンプトの末尾に足すセクションを返します。
// 指示が空の場合は空文字を返すので、呼び出し側は素直に連結できます。
func PromptSection(text string) string {
	if text = strings.TrimSpace(text); text == "" {
		return ""
	}
	return fmt.Sprintf("\n\n%s\n%s", promptHeading, text)
}
