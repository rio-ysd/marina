package main

import (
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"

	"github.com/yoshida-rio/marina/internal/app"
	"github.com/yoshida-rio/marina/internal/config"
)

var marinaApp *app.App

func init() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	marinaApp, err = app.New(cfg)
	if err != nil {
		log.Fatalf("init app: %v", err)
	}
}

// handler はAPI Gateway(REST API/HTTP API プロキシ統合)からのイベントをHandler.ServeHTTPに橋渡しします。
func handler(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, req.HTTPMethod, req.Path, strings.NewReader(req.Body))
	if err != nil {
		return events.APIGatewayProxyResponse{StatusCode: http.StatusBadRequest}, nil
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	recorder := httptest.NewRecorder()
	marinaApp.SlackHandler.ServeHTTP(recorder, httpReq)

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

func main() {
	defer marinaApp.DB.Close()
	lambda.Start(handler)
}
