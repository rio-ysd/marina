package slack

// 非同期処理に回すSlackリクエストの種別。
const (
	AsyncKindEvents       = "events"
	AsyncKindInteractions = "interactions"
)

// AsyncRequest は受信Lambda(cmd/lambda)からワーカーLambda(cmd/eventworker)へ
// 引き渡すSlackリクエストです。
//
// Lambdaはハンドラがreturnすると実行環境が凍結されるため、Slackへ3秒以内にackしてから
// バックグラウンドgoroutineで処理を続ける方式が使えません。そのため受信側で署名検証と
// ackだけを行い、実処理(Claude呼び出し・Slackへの投稿)は別Lambdaを非同期invokeして行います。
// Bodyは署名検証済みの生ボディなので、ワーカー側では再検証しません。
type AsyncRequest struct {
	Kind string `json:"kind"`
	Body string `json:"body"`
}
