// cmd/lambda はAPI Gateway経由でSlackからのリクエストを受け取るLambdaエントリポイントです。
// Slackイベントとボタン押下は署名検証とackだけを行い、実処理(Claude呼び出し・Slackへの投稿)は
// ワーカーLambda(cmd/eventworker)を非同期invokeして任せます。Lambdaはハンドラがreturnすると
// 実行環境が凍結されるため、ack後にバックグラウンドgoroutineで処理を続ける方式は使えません。
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	lambdasdk "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/yoshida-rio/marina/internal/app"
	"github.com/yoshida-rio/marina/internal/config"
	marinaslack "github.com/yoshida-rio/marina/internal/slack"
)

const (
	slackEventsPath       = "/slack/events"
	slackInteractionsPath = "/slack/interactions"
)

var (
	marinaApp    *app.App
	lambdaClient *lambdasdk.Client
	// workerFunctionName が未設定の場合はワーカーへ渡さず、その場で同期処理します
	// (ローカル検証やワーカー未デプロイ時のフォールバック)。
	workerFunctionName string
)

func init() {
	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	marinaApp, err = app.New(cfg)
	if err != nil {
		log.Fatalf("init app: %v", err)
	}

	workerFunctionName = os.Getenv("WORKER_FUNCTION_NAME")
	if workerFunctionName != "" {
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			log.Fatalf("load aws config: %v", err)
		}
		lambdaClient = lambdasdk.NewFromConfig(awsCfg)
	} else {
		log.Print("WORKER_FUNCTION_NAME is not set: slack requests will be processed synchronously")
	}
}

// handler はAPI Gateway(REST API/HTTP API プロキシ統合、ペイロード形式1.0)からのイベントを処理します。
func handler(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	body, err := requestBody(req)
	if err != nil {
		log.Printf("decode body: %v", err)
		return textResponse(http.StatusBadRequest, "failed to decode body"), nil
	}

	switch req.Path {
	case slackEventsPath:
		return handleSlackEvents(ctx, req, body)
	case slackInteractionsPath:
		return handleSlackInteractions(ctx, req, body)
	default:
		// /healthz や /mf/oauth/... は短時間で終わるためMuxで同期処理する。
		return serveMux(ctx, req, body)
	}
}

// handleSlackEvents はEvents APIのリクエストを処理します。
// url_verificationチャレンジのみ同期で応答し、それ以外はワーカーへ回します。
func handleSlackEvents(ctx context.Context, req events.APIGatewayProxyRequest, body []byte) (events.APIGatewayProxyResponse, error) {
	if err := marinaslack.VerifySignature(httpHeader(req), body, marinaApp.Config.SlackSigningSecret); err != nil {
		log.Printf("slack signature verification failed: %v", err)
		return textResponse(http.StatusUnauthorized, "invalid signature"), nil
	}

	var probe struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return textResponse(http.StatusBadRequest, "failed to parse event"), nil
	}
	if probe.Type == "url_verification" {
		return textResponse(http.StatusOK, probe.Challenge), nil
	}

	return dispatch(ctx, marinaslack.AsyncKindEvents, body)
}

// handleSlackInteractions はInteractivity(ボタン押下・モーダルの送信)を処理します。
// App Homeのモーダルはtrigger_idの有効期限が3秒しかなく、ワーカーLambdaの起動を待つと開けないため、
// その種類だけはワーカーへ回さずここで処理します(Slack APIを1〜2回叩くだけで済む処理に限ります)。
func handleSlackInteractions(ctx context.Context, req events.APIGatewayProxyRequest, body []byte) (events.APIGatewayProxyResponse, error) {
	if err := marinaslack.VerifySignature(httpHeader(req), body, marinaApp.Config.SlackSigningSecret); err != nil {
		log.Printf("slack signature verification failed: %v", err)
		return textResponse(http.StatusUnauthorized, "invalid signature"), nil
	}

	handled, err := marinaApp.InteractionHandler.TryHandleSync(body)
	if err != nil {
		log.Printf("handle interaction synchronously: %v", err)
		return textResponse(http.StatusBadRequest, "failed to parse payload"), nil
	}
	if handled {
		// モーダルの送信(view_submission)はボディが空のときだけ閉じる。
		// テキストを返すとSlack側でパースに失敗し「接続に問題が発生しました」と表示される。
		return events.APIGatewayProxyResponse{StatusCode: http.StatusOK}, nil
	}

	return dispatch(ctx, marinaslack.AsyncKindInteractions, body)
}

// dispatch はワーカーLambdaを非同期invokeします。ワーカー未設定の場合はその場で同期処理します。
// invokeに失敗した場合は500を返し、Slackの再送に任せます(黙って取りこぼすのを避けるため)。
func dispatch(ctx context.Context, kind string, body []byte) (events.APIGatewayProxyResponse, error) {
	asyncReq := marinaslack.AsyncRequest{Kind: kind, Body: string(body)}

	if lambdaClient == nil {
		if err := marinaApp.HandleAsyncRequest(asyncReq); err != nil {
			log.Printf("handle slack request (%s): %v", kind, err)
		}
		return textResponse(http.StatusOK, "ok"), nil
	}

	payload, err := json.Marshal(asyncReq)
	if err != nil {
		log.Printf("marshal async request: %v", err)
		return textResponse(http.StatusInternalServerError, "failed to marshal request"), nil
	}
	if _, err := lambdaClient.Invoke(ctx, &lambdasdk.InvokeInput{
		FunctionName:   &workerFunctionName,
		InvocationType: lambdatypes.InvocationTypeEvent,
		Payload:        payload,
	}); err != nil {
		log.Printf("invoke worker (%s): %v", kind, err)
		return textResponse(http.StatusInternalServerError, "failed to dispatch"), nil
	}
	return textResponse(http.StatusOK, "ok"), nil
}

// serveMux はSlack以外のパス(ヘルスチェック・MoneyForward OAuthコールバック)をMuxへ橋渡しします。
func serveMux(ctx context.Context, req events.APIGatewayProxyRequest, body []byte) (events.APIGatewayProxyResponse, error) {
	path := req.Path
	if len(req.QueryStringParameters) > 0 {
		query := url.Values{}
		for k, v := range req.QueryStringParameters {
			query.Set(k, v)
		}
		path += "?" + query.Encode()
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.HTTPMethod, path, strings.NewReader(string(body)))
	if err != nil {
		return events.APIGatewayProxyResponse{StatusCode: http.StatusBadRequest}, nil
	}
	httpReq.Header = httpHeader(req)

	recorder := httptest.NewRecorder()
	marinaApp.Mux().ServeHTTP(recorder, httpReq)

	headers := map[string]string{}
	for k := range recorder.Header() {
		headers[k] = recorder.Header().Get(k)
	}
	return events.APIGatewayProxyResponse{
		StatusCode: recorder.Code,
		Headers:    headers,
		Body:       recorder.Body.String(),
	}, nil
}

func requestBody(req events.APIGatewayProxyRequest) ([]byte, error) {
	if !req.IsBase64Encoded {
		return []byte(req.Body), nil
	}
	return base64.StdEncoding.DecodeString(req.Body)
}

func httpHeader(req events.APIGatewayProxyRequest) http.Header {
	header := http.Header{}
	for k, v := range req.Headers {
		header.Set(k, v)
	}
	return header
}

func textResponse(status int, body string) events.APIGatewayProxyResponse {
	return events.APIGatewayProxyResponse{
		StatusCode: status,
		Headers:    map[string]string{"Content-Type": "text/plain; charset=utf-8"},
		Body:       body,
	}
}

func main() {
	defer marinaApp.DB.Close()
	lambda.Start(handler)
}
