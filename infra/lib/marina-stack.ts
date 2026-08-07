import * as path from 'path';
import * as cdk from 'aws-cdk-lib';
import * as apigwv2 from 'aws-cdk-lib/aws-apigatewayv2';
import * as apigwv2Integrations from 'aws-cdk-lib/aws-apigatewayv2-integrations';
import * as ec2 from 'aws-cdk-lib/aws-ec2';
import * as events from 'aws-cdk-lib/aws-events';
import * as targets from 'aws-cdk-lib/aws-events-targets';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as logs from 'aws-cdk-lib/aws-logs';
import * as rds from 'aws-cdk-lib/aws-rds';
import * as secretsmanager from 'aws-cdk-lib/aws-secretsmanager';
import { Construct } from 'constructs';

// scripts/build-lambdas.sh が出力するディレクトリ。
const distDir = path.join(__dirname, '..', '..', 'dist');

// Bedrockのリージョン(モデルアクセスを有効化したリージョン)。
const bedrockRegion = 'us-east-1';
const anthropicModel = 'anthropic.claude-sonnet-5';

/**
 * marina本体のスタック。
 *
 * LambdaはVPC外に置き、RDSをパブリック公開して接続する構成にしている
 * (NAT GatewayもNATインスタンスも不要にするため)。
 * その代わりRDSのセキュリティグループは全開放になるため、
 * 強固な自動生成パスワードとTLS必須(require_secure_transport)で保護している。
 */
export class MarinaStack extends cdk.Stack {
  constructor(scope: Construct, id: string, props?: cdk.StackProps) {
    super(scope, id, props);

    // RDSはVPCが必須。Lambdaは外に出すのでパブリックサブネットのみ、NATは作らない。
    const vpc = new ec2.Vpc(this, 'Vpc', {
      maxAzs: 2,
      natGateways: 0,
      subnetConfiguration: [{ name: 'public', subnetType: ec2.SubnetType.PUBLIC, cidrMask: 24 }],
    });

    const dbSecurityGroup = new ec2.SecurityGroup(this, 'DbSecurityGroup', {
      vpc,
      description: 'marina RDS MySQL',
      allowAllOutbound: false,
    });
    // LambdaをVPC外に置いているため送信元IPを固定できず、全開放が避けられない。
    // 認証はSecrets Managerが生成する32文字のパスワード、通信はTLS必須で保護する。
    dbSecurityGroup.addIngressRule(
      ec2.Peer.anyIpv4(),
      ec2.Port.tcp(3306),
      'Lambda (outside VPC) has no fixed source IP; protected by generated password + required TLS',
    );

    // TLSなしの接続を拒否する。
    const parameterGroup = new rds.ParameterGroup(this, 'DbParameterGroup', {
      engine: rds.DatabaseInstanceEngine.mysql({ version: rds.MysqlEngineVersion.VER_8_0_46 }),
      description: 'marina MySQL parameters (require TLS)',
      parameters: { require_secure_transport: 'ON' },
    });

    const database = new rds.DatabaseInstance(this, 'Database', {
      engine: rds.DatabaseInstanceEngine.mysql({ version: rds.MysqlEngineVersion.VER_8_0_46 }),
      instanceType: ec2.InstanceType.of(ec2.InstanceClass.BURSTABLE4_GRAVITON, ec2.InstanceSize.MICRO),
      vpc,
      vpcSubnets: { subnetType: ec2.SubnetType.PUBLIC },
      publiclyAccessible: true,
      securityGroups: [dbSecurityGroup],
      parameterGroup,
      databaseName: 'marina',
      credentials: rds.Credentials.fromGeneratedSecret('marina', { secretName: 'marina/db' }),
      allocatedStorage: 20,
      storageType: rds.StorageType.GP3,
      storageEncrypted: true,
      backupRetention: cdk.Duration.days(7),
      deletionProtection: true,
      removalPolicy: cdk.RemovalPolicy.SNAPSHOT,
      cloudwatchLogsExports: ['error'],
    });

    // Slackトークン等はLambdaの環境変数に平文で置かず、Secrets Managerから起動時に読む
    // (internal/config.LoadSecretsIntoEnv)。値は初回デプロイ後に手動で投入する。
    // generateStringKeyは有効なJSONを最初から入れておくためのダミー項目。
    const appSecret = new secretsmanager.Secret(this, 'AppSecret', {
      secretName: 'marina/app',
      description: 'marina app config: Slack tokens, Google service account, MoneyForward OAuth',
      generateSecretString: {
        secretStringTemplate: JSON.stringify({
          SLACK_BOT_TOKEN: '',
          SLACK_SIGNING_SECRET: '',
          SLACK_USER_OAUTH_TOKEN: '',
          PROXY_REPLY_TARGET_USER_ID: '',
          PROXY_REPLY_CHANNEL_IDS: '',
          PROXY_REPLY_INCLUDE_DM: 'true',
          MORNING_DIGEST_SLACK_CHANNEL: '',
          GOOGLE_SERVICE_ACCOUNT_JSON: '',
          GOOGLE_IMPERSONATED_USER: '',
          MF_CLIENT_ID: '',
          MF_CLIENT_SECRET: '',
          MF_OAUTH_REDIRECT_URI: '',
        }),
        generateStringKey: 'PLACEHOLDER_UNUSED',
      },
    });

    const commonEnvironment = {
      MARINA_SECRET_ID: appSecret.secretName,
      DB_SECRET_ID: database.secret!.secretName,
      ANTHROPIC_MODEL: anthropicModel,
      BEDROCK_REGION: bedrockRegion,
    };

    const makeFunction = (
      id: string,
      dist: string,
      options: { timeout: cdk.Duration; memorySize: number; description: string },
    ): lambda.Function => {
      const fn = new lambda.Function(this, id, {
        runtime: lambda.Runtime.PROVIDED_AL2023,
        architecture: lambda.Architecture.ARM_64,
        handler: 'bootstrap',
        code: lambda.Code.fromAsset(path.join(distDir, dist)),
        environment: commonEnvironment,
        timeout: options.timeout,
        memorySize: options.memorySize,
        description: options.description,
        logGroup: new logs.LogGroup(this, `${id}Logs`, {
          retention: logs.RetentionDays.ONE_MONTH,
          removalPolicy: cdk.RemovalPolicy.DESTROY,
        }),
      });
      appSecret.grantRead(fn);
      database.secret!.grantRead(fn);
      return fn;
    };

    // Claude(Bedrock)を呼ぶ関数に付与する権限。
    const bedrockPolicy = new iam.PolicyStatement({
      actions: ['bedrock:InvokeModel', 'bedrock:InvokeModelWithResponseStream'],
      resources: [
        'arn:aws:bedrock:*::foundation-model/*',
        `arn:aws:bedrock:*:${this.account}:inference-profile/*`,
      ],
    });

    // Slackイベント/ボタン押下の実処理。受信Lambdaから非同期invokeされる。
    const eventWorker = makeFunction('EventWorker', 'eventworker', {
      timeout: cdk.Duration.minutes(3),
      memorySize: 1024,
      description: 'marina: process slack events/interactions (Claude, post to Slack)',
    });
    eventWorker.addToRolePolicy(bedrockPolicy);

    // API Gatewayから呼ばれる受信Lambda。署名検証とackのみを行う。
    const slackReceiver = makeFunction('SlackReceiver', 'lambda', {
      timeout: cdk.Duration.seconds(15),
      memorySize: 512,
      description: 'marina: verify slack signature, ack, dispatch to worker',
    });
    slackReceiver.addEnvironment('WORKER_FUNCTION_NAME', eventWorker.functionName);
    eventWorker.grantInvoke(slackReceiver);
    // MoneyForwardのOAuthコールバックは受信Lambdaが同期処理するため、こちらにも権限が必要。
    slackReceiver.addToRolePolicy(bedrockPolicy);

    const morningDigest = makeFunction('MorningDigest', 'morningdigest', {
      timeout: cdk.Duration.minutes(5),
      memorySize: 1024,
      description: 'marina: morning gmail digest',
    });
    morningDigest.addToRolePolicy(bedrockPolicy);

    const reminderWorker = makeFunction('ReminderWorker', 'reminderworker', {
      timeout: cdk.Duration.minutes(1),
      memorySize: 512,
      description: 'marina: send due reminders to slack',
    });

    // Slack Events API / Interactivity / MoneyForward OAuthコールバックの受け口。
    // cmd/lambdaはAPIGatewayProxyRequest(ペイロード形式1.0)を前提にしているため1.0を指定する。
    const httpApi = new apigwv2.HttpApi(this, 'HttpApi', {
      apiName: 'marina',
      description: 'marina slack endpoint',
      defaultIntegration: new apigwv2Integrations.HttpLambdaIntegration('DefaultIntegration', slackReceiver, {
        payloadFormatVersion: apigwv2.PayloadFormatVersion.VERSION_1_0,
      }),
    });

    // 平日9:00 JST = 0:00 UTC (JSTはサマータイムなし)。
    new events.Rule(this, 'MorningDigestSchedule', {
      description: 'marina: weekdays 09:00 JST',
      schedule: events.Schedule.cron({ minute: '0', hour: '0', weekDay: 'MON-FRI' }),
      targets: [new targets.LambdaFunction(morningDigest)],
    });

    new events.Rule(this, 'ReminderSchedule', {
      description: 'marina: check due reminders',
      schedule: events.Schedule.rate(cdk.Duration.minutes(5)),
      targets: [new targets.LambdaFunction(reminderWorker)],
    });

    new cdk.CfnOutput(this, 'ApiEndpoint', {
      value: httpApi.apiEndpoint,
      description: 'Slack Event Subscriptions / Interactivity のRequest URLに使うベースURL',
    });
    new cdk.CfnOutput(this, 'DbEndpoint', {
      value: database.dbInstanceEndpointAddress,
      description: 'RDSのエンドポイント(マイグレーション実行先)',
    });
    new cdk.CfnOutput(this, 'DbSecretName', {
      value: database.secret!.secretName,
      description: 'DB認証情報のSecrets ManagerシークレットID',
    });
    new cdk.CfnOutput(this, 'AppSecretName', {
      value: appSecret.secretName,
      description: 'アプリ設定のSecrets ManagerシークレットID(初回デプロイ後に値を投入する)',
    });
  }
}
