package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// LoadSecretsIntoEnv はSecrets Managerに保存した設定値を環境変数へ展開します。
// Lambdaの環境変数にトークンを平文で置かないため、起動時にこれを呼んでからLoadします。
//
//   - MARINA_SECRET_ID: アプリ設定のシークレットID。SLACK_BOT_TOKEN等をキーに持つJSONオブジェクト
//   - DB_SECRET_ID: RDSが管理するDB認証情報のシークレットID。DB_DSNを組み立てて設定する
//
// どちらも未設定なら何もしません(ローカルは.envの環境変数をそのまま使います)。
// 既に環境変数が設定されている項目は上書きしません。
func LoadSecretsIntoEnv(ctx context.Context) error {
	appSecretID := os.Getenv("MARINA_SECRET_ID")
	dbSecretID := os.Getenv("DB_SECRET_ID")
	if appSecretID == "" && dbSecretID == "" {
		return nil
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("load aws config: %w", err)
	}
	client := secretsmanager.NewFromConfig(awsCfg)

	if appSecretID != "" {
		values, err := fetchSecretJSON(ctx, client, appSecretID)
		if err != nil {
			return fmt.Errorf("load app secret: %w", err)
		}
		for k, v := range values {
			setEnvIfEmpty(k, v)
		}
	}

	if dbSecretID != "" && os.Getenv("DB_DSN") == "" {
		values, err := fetchSecretJSON(ctx, client, dbSecretID)
		if err != nil {
			return fmt.Errorf("load db secret: %w", err)
		}
		dsn, err := buildMySQLDSN(values)
		if err != nil {
			return fmt.Errorf("build db dsn: %w", err)
		}
		_ = os.Setenv("DB_DSN", dsn)
	}

	return nil
}

func fetchSecretJSON(ctx context.Context, client *secretsmanager.Client, secretID string) (map[string]string, error) {
	out, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &secretID})
	if err != nil {
		return nil, fmt.Errorf("get secret value %s: %w", secretID, err)
	}
	if out.SecretString == nil {
		return nil, fmt.Errorf("secret %s has no string value", secretID)
	}

	// 数値やbooleanが混ざっていても文字列として扱えるようanyで受けます。
	var raw map[string]any
	if err := json.Unmarshal([]byte(*out.SecretString), &raw); err != nil {
		return nil, fmt.Errorf("parse secret %s as json object: %w", secretID, err)
	}

	values := make(map[string]string, len(raw))
	for k, v := range raw {
		switch typed := v.(type) {
		case string:
			values[k] = typed
		case float64:
			values[k] = fmt.Sprintf("%.0f", typed)
		case bool:
			values[k] = fmt.Sprintf("%t", typed)
		}
	}
	return values, nil
}

// buildMySQLDSN はRDSが管理するシークレット(username/password/host/port/dbname)からDSNを組み立てます。
// RDSをパブリック公開しているため、通信が平文にならないようTLSを必須にします。
// tls=skip-verifyはAmazon RDSのCAがシステムの信頼ストアに入っていないための指定で、
// 暗号化はされますがサーバー証明書の検証は行いません。
func buildMySQLDSN(values map[string]string) (string, error) {
	user, password, host := values["username"], values["password"], values["host"]
	if user == "" || password == "" || host == "" {
		return "", fmt.Errorf("db secret must contain username, password and host")
	}
	port := values["port"]
	if port == "" {
		port = "3306"
	}
	dbName := values["dbname"]
	if dbName == "" {
		dbName = "marina"
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&loc=Local&tls=skip-verify",
		user, url.QueryEscape(password), host, port, dbName), nil
}

func setEnvIfEmpty(key, value string) {
	if value == "" || os.Getenv(key) != "" {
		return
	}
	_ = os.Setenv(key, value)
}
