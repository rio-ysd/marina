import * as cdk from 'aws-cdk-lib';
import * as iam from 'aws-cdk-lib/aws-iam';
import { Construct } from 'constructs';

export interface GithubOidcStackProps extends cdk.StackProps {
  readonly githubOwner: string;
  readonly githubRepo: string;
}

/**
 * GitHub ActionsがOIDCでAWSにアクセスするためのIAMロールを作成するスタック。
 *
 * アクセスキーを長期発行しないため、GitHub Secretsに秘密情報を置かずに済む。
 * このスタックだけは初回に手元(管理者権限)からデプロイする必要がある
 * (デプロイ用ロールが無い状態ではGitHub Actionsから作れないため)。
 */
export class GithubOidcStack extends cdk.Stack {
  constructor(scope: Construct, id: string, props: GithubOidcStackProps) {
    super(scope, id, props);

    const provider = new iam.OpenIdConnectProvider(this, 'GithubOidcProvider', {
      url: 'https://token.actions.githubusercontent.com',
      clientIds: ['sts.amazonaws.com'],
    });

    // 許可するsubクレーム。GitHubは環境によって2つの形式を送ってくるため両方に対応する。
    //   従来形式  : repo:<owner>/<repo>:ref:refs/heads/main
    //   immutable : repo:<owner>@<owner_id>/<repo>@<repo_id>:ref:refs/heads/main
    // デプロイはmainブランチからのみ許可する(PRブランチからロールを引き受けられないようにするため)。
    const subjectPatterns = [
      `repo:${props.githubOwner}/${props.githubRepo}:ref:refs/heads/main`,
      `repo:${props.githubOwner}@*/${props.githubRepo}@*:ref:refs/heads/main`,
    ];

    const role = new iam.Role(this, 'DeployRole', {
      roleName: 'marina-github-actions-deploy',
      description: 'Assumed by GitHub Actions to deploy marina via CDK',
      maxSessionDuration: cdk.Duration.hours(1),
      assumedBy: new iam.WebIdentityPrincipal(provider.openIdConnectProviderArn, {
        StringEquals: {
          'token.actions.githubusercontent.com:aud': 'sts.amazonaws.com',
        },
        StringLike: {
          'token.actions.githubusercontent.com:sub': subjectPatterns,
        },
      }),
    });

    // CDKデプロイはbootstrapが作ったcdk-*ロールを引き受けて行うため、
    // デプロイロール自体には広い権限を持たせない。
    role.addToPolicy(
      new iam.PolicyStatement({
        sid: 'AssumeCdkBootstrapRoles',
        actions: ['sts:AssumeRole'],
        resources: [`arn:aws:iam::${this.account}:role/cdk-*`],
      }),
    );
    // マイグレーション実行時にDB接続情報を読むため。
    role.addToPolicy(
      new iam.PolicyStatement({
        sid: 'ReadMarinaSecrets',
        actions: ['secretsmanager:GetSecretValue', 'secretsmanager:DescribeSecret'],
        resources: [`arn:aws:secretsmanager:${this.region}:${this.account}:secret:marina/*`],
      }),
    );
    // スタック出力(APIエンドポイント等)の参照とデプロイ後の疎通確認用。
    role.addToPolicy(
      new iam.PolicyStatement({
        sid: 'ReadStackOutputs',
        actions: ['cloudformation:DescribeStacks'],
        resources: [`arn:aws:cloudformation:${this.region}:${this.account}:stack/Marina*/*`],
      }),
    );

    new cdk.CfnOutput(this, 'DeployRoleArn', {
      value: role.roleArn,
      description: 'GitHub Actionsの AWS_DEPLOY_ROLE_ARN に設定する値',
    });
  }
}
