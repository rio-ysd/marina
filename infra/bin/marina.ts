#!/usr/bin/env node
import * as cdk from 'aws-cdk-lib';
import { GithubOidcStack } from '../lib/github-oidc-stack';
import { MarinaStack } from '../lib/marina-stack';

const app = new cdk.App();

// デプロイ先リージョンは固定する。手元のAWS_REGION/プロファイル既定値に引きずられて
// 意図しないリージョンにスタックが作られるのを防ぐため(Bedrockのみus-east-1を利用する)。
const env = {
  account: process.env.CDK_DEFAULT_ACCOUNT ?? '748671410846',
  region: 'ap-northeast-1',
};

// GitHub ActionsからのデプロイにOIDCを使うためのIAMロール。
// 初回だけ手元(管理者権限)からデプロイし、以降はGitHub Actionsがこのロールを引き受ける。
new GithubOidcStack(app, 'MarinaGithubOidcStack', {
  env,
  githubOwner: 'rio-ysd',
  githubRepo: 'marina',
  description: 'GitHub Actions OIDC role for deploying marina',
});

new MarinaStack(app, 'MarinaStack', {
  env,
  description: 'marina: Slack secretary agent (Lambda + API Gateway + RDS MySQL)',
});

app.synth();
