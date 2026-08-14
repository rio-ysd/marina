package slack

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/slack-go/slack"

	"github.com/yoshida-rio/marina/internal/instructions"
	"github.com/yoshida-rio/marina/internal/storage"
)

// App Homeのボタン・モーダルの識別子。InteractionHandlerから参照します。
const (
	// ActionEditInstructions はHomeタブの「編集する」ボタンのaction_idです。
	ActionEditInstructions = "home_edit_instructions"
	// CallbackEditInstructions は編集モーダルのcallback_idです。
	CallbackEditInstructions = "home_edit_instructions_modal"

	blockIDInstructions  = "home_instructions_block"
	actionIDInstructions = "home_instructions_input"
)

// jst はHomeタブに更新日時を表示するためのタイムゾーンです(LambdaのTZはUTCのため明示)。
var jst = time.FixedZone("JST", 9*60*60)

// InstructionStore はApp Homeで編集する追加指示の読み書きです。
// internal/instructions.Storeが実装します。
type InstructionStore interface {
	Get(ctx context.Context) (storage.Setting, error)
	Save(ctx context.Context, text, updatedBy string) error
}

// HomeHandler はSlack App Homeのタブ表示と、そこからの追加指示の編集を処理します。
// 追加指示はSlackでの応答と朝のメールダイジェストの両方に反映されます。
type HomeHandler struct {
	client *slack.Client
	store  InstructionStore
	// editorUserID は編集を許可するSlackユーザーID。
	// 空の場合は誰にも編集させません(Homeタブは閲覧のみになります)。
	editorUserID string
}

// NewHomeHandler はHomeHandlerを構築します。
func NewHomeHandler(client *slack.Client, store InstructionStore, editorUserID string) *HomeHandler {
	return &HomeHandler{client: client, store: store, editorUserID: strings.TrimSpace(editorUserID)}
}

// CanEdit は指定ユーザーが追加指示を編集できるかを返します。
func (h *HomeHandler) CanEdit(userID string) bool {
	return h != nil && h.editorUserID != "" && h.editorUserID == userID
}

// Publish は指定ユーザーのHomeタブを最新の内容で描き直します。
func (h *HomeHandler) Publish(ctx context.Context, userID string) error {
	if h == nil || userID == "" {
		return nil
	}
	setting, err := h.store.Get(ctx)
	if err != nil {
		return fmt.Errorf("load instructions: %w", err)
	}
	if _, err := h.client.PublishViewContext(ctx, slack.PublishViewContextRequest{
		UserID: userID,
		View: slack.HomeTabViewRequest{
			Type:   slack.VTHomeTab,
			Blocks: slack.Blocks{BlockSet: h.homeBlocks(setting, userID)},
		},
	}); err != nil {
		return fmt.Errorf("publish home view: %w", err)
	}
	return nil
}

// OpenEditModal は「編集する」ボタンから編集モーダルを開きます。
// trigger_idの有効期限は3秒なので、呼び出し側は待たせずにここへ到達させる必要があります。
func (h *HomeHandler) OpenEditModal(ctx context.Context, triggerID, actorUserID string) error {
	if h == nil {
		return nil
	}
	if !h.CanEdit(actorUserID) {
		// ボタンは編集者にしか出していないので、ここに来るのは古い画面からの押下。
		log.Printf("home: user %s is not allowed to edit instructions", actorUserID)
		return nil
	}
	setting, err := h.store.Get(ctx)
	if err != nil {
		return fmt.Errorf("load instructions: %w", err)
	}
	if _, err := h.client.OpenViewContext(ctx, triggerID, editModalView(setting.Value)); err != nil {
		return fmt.Errorf("open edit modal: %w", err)
	}
	return nil
}

// SaveFromSubmission は編集モーダルの送信内容を保存し、Homeタブを描き直します。
func (h *HomeHandler) SaveFromSubmission(ctx context.Context, callback slack.InteractionCallback) error {
	if h == nil {
		return nil
	}
	actor := callback.User.ID
	if !h.CanEdit(actor) {
		log.Printf("home: user %s is not allowed to edit instructions", actor)
		return nil
	}

	text := instructionsFromState(callback.View.State)
	if err := h.store.Save(ctx, text, actor); err != nil {
		return fmt.Errorf("save instructions: %w", err)
	}
	log.Printf("home: instructions updated by %s (%d chars)", actor, len([]rune(text)))

	if err := h.Publish(ctx, actor); err != nil {
		// 保存は済んでいるので、描き直しの失敗はログのみ(次にHomeを開けば反映される)。
		log.Printf("home: republish after save failed: %v", err)
	}
	return nil
}

// instructionsFromState はモーダルの入力値を取り出します。
// 未入力の場合は空文字になり、「指示なし」として保存されます。
func instructionsFromState(state *slack.ViewState) string {
	if state == nil {
		return ""
	}
	return strings.TrimSpace(state.Values[blockIDInstructions][actionIDInstructions].Value)
}

// homeBlocks はHomeタブの表示を組み立てます。
// 編集ボタンは編集者にだけ出し、それ以外のユーザーには誰に頼めばよいかを示します。
func (h *HomeHandler) homeBlocks(setting storage.Setting, viewerUserID string) []slack.Block {
	blocks := []slack.Block{
		slack.NewHeaderBlock(slack.NewTextBlockObject(slack.PlainTextType, "marinaの設定", false, false)),
		mrkdwnSection("ここに書いた指示は、*Slackでの応答* と *朝のメールダイジェスト* の両方に反映されます。" +
			"コードを変更せずに運用ルールを足せます。"),
		slack.NewDividerBlock(),
		mrkdwnSection("*追加の運用ルール*\n" + instructionsDisplay(setting.Value)),
	}

	if setting.UpdatedBy != "" {
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject(slack.MarkdownType,
				fmt.Sprintf("最終更新: <@%s> ・ %s", setting.UpdatedBy, setting.UpdatedAt.In(jst).Format("2006/01/02 15:04")),
				false, false),
		))
	}

	switch {
	case h.CanEdit(viewerUserID):
		edit := slack.NewButtonBlockElement(ActionEditInstructions, "",
			slack.NewTextBlockObject(slack.PlainTextType, "編集する", false, false))
		edit.Style = slack.StylePrimary
		blocks = append(blocks, slack.NewActionBlock("home_instructions_actions", edit))
	case h.editorUserID != "":
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject(slack.MarkdownType,
				fmt.Sprintf("編集できるのは <@%s> のみです。", h.editorUserID), false, false),
		))
	default:
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject(slack.MarkdownType,
				"編集できるユーザーが設定されていません(`HOME_ADMIN_SLACK_USER_ID`)。", false, false),
		))
	}

	return append(blocks,
		slack.NewDividerBlock(),
		mrkdwnSection("*書き方の例*\n"+
			"```\nSALON BOARD関連のメールは重要ではないので、既読にするだけでよい\n"+
			"経理からのメールは必ず要返信に入れる\n```"),
	)
}

// instructionsDisplay は保存されている指示を引用形式で表示します。未設定の場合はその旨を返します。
func instructionsDisplay(value string) string {
	if value = strings.TrimSpace(value); value == "" {
		return "_まだ設定されていません。_"
	}
	lines := strings.Split(value, "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

// editModalView は追加指示の編集モーダルを組み立てます。
func editModalView(current string) slack.ModalViewRequest {
	input := slack.NewPlainTextInputBlockElement(
		slack.NewTextBlockObject(slack.PlainTextType, "例: SALON BOARD関連のメールは重要ではないので、既読にするだけでよい", false, false),
		actionIDInstructions)
	input.Multiline = true
	input.InitialValue = current
	input.MaxLength = instructions.MaxLen

	block := slack.NewInputBlock(blockIDInstructions,
		slack.NewTextBlockObject(slack.PlainTextType, "追加の運用ルール", false, false),
		slack.NewTextBlockObject(slack.PlainTextType, "1行に1つ書いてください。空にすると指示なしに戻ります。", false, false),
		input)
	// 空欄での保存(指示の削除)を許すため必須にしない。
	block.Optional = true

	return slack.ModalViewRequest{
		Type:       slack.VTModal,
		CallbackID: CallbackEditInstructions,
		Title:      slack.NewTextBlockObject(slack.PlainTextType, "追加の運用ルール", false, false),
		Submit:     slack.NewTextBlockObject(slack.PlainTextType, "保存", false, false),
		Close:      slack.NewTextBlockObject(slack.PlainTextType, "キャンセル", false, false),
		Blocks:     slack.Blocks{BlockSet: []slack.Block{block}},
	}
}

// mrkdwnSection はmrkdwnのsectionブロックを作ります。
func mrkdwnSection(text string) *slack.SectionBlock {
	return slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, text, false, false), nil, nil)
}
