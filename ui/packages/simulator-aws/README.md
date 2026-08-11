# @sockerless/ui-simulator-aws

The AWS Management Console this simulator serves: a Cloudscape shell
(`src/console/`) with per-service pages under `src/pages/`, routed in
`src/main.tsx` (React Router 7).

## How it reads AWS

Every page reads the **real AWS APIs** — the same operations, wire protocols,
and response shapes a console pointed at real AWS reads — over federated,
SigV4-signed requests. `src/console/federation.ts` exchanges the operator's
assertion for temporary credentials at the Security Token Service
(`AssumeRoleWithWebIdentity`) and `src/console/sigv4.ts` signs with them; only
the coordinates (the endpoint base URLs and the role to assume, read from
`/ui/config.json`) differ between the simulator and real AWS. There is no
simulator-only endpoint, no `/sim/*` dashboard route, and no
simulator-versus-cloud branch anywhere in the data plane.

`src/api.ts` holds one reader (and writer) per AWS operation the console
drives, named for the operation it calls.

## Pages

The side navigation (`src/console/serviceCatalog.ts`) lists AWS's own service
catalog, grouped the way AWS's "All services" menu groups it. A service the
simulator implements links to its page; a service it does not links to the
honest "not supported" page and carries a "Not supported" pill. That flag is a
claim about the simulator's real coverage: it is only ever set for a service
the simulator registers no usable read surface for.

| Route | Service | Leading operation |
| --- | --- | --- |
| `/ui/` | Overview | per-service list reads |
| `/ui/ec2`, `/ui/ec2/:instanceId` | Amazon EC2 | `DescribeInstances`, `DescribeVolumes` |
| `/ui/autoscaling` | Amazon EC2 Auto Scaling | `DescribeAutoScalingGroups` |
| `/ui/lambda`, `/ui/lambda/:name` | AWS Lambda | `ListFunctions`, `GetFunction` |
| `/ui/batch` | AWS Batch | `DescribeJobQueues`, `DescribeComputeEnvironments` |
| `/ui/ecs`, `/ui/ecs/:taskArn` | Amazon Elastic Container Service | `ListTasks`, `DescribeTasks` |
| `/ui/ecr`, `/ui/ecr/:name` | Amazon Elastic Container Registry | `DescribeRepositories`, `DescribeImages` |
| `/ui/s3`, `/ui/s3/:name` | Amazon Simple Storage Service | `ListBuckets`, `ListObjectsV2` |
| `/ui/efs`, `/ui/efs/:fileSystemId` | Amazon Elastic File System | `DescribeFileSystems`, `DescribeMountTargets` |
| `/ui/rds` | Amazon Relational Database Service | `DescribeDBInstances`, `DescribeDBClusters` |
| `/ui/dynamodb`, `/ui/dynamodb/:name` | Amazon DynamoDB | `ListTables`, `DescribeTable` |
| `/ui/elasticache` | Amazon ElastiCache | `DescribeCacheClusters` |
| `/ui/vpc`, `/ui/vpc/:vpcId` | Amazon Virtual Private Cloud | `DescribeVpcs`, `DescribeSubnets`, `DescribeSecurityGroups` |
| `/ui/cloudfront` | Amazon CloudFront | `ListDistributions` |
| `/ui/route53`, `/ui/route53/:hostedZoneId` | Amazon Route 53 | `ListHostedZones`, `ListResourceRecordSets` |
| `/ui/apigateway` | Amazon API Gateway | `GetRestApis`, `GetApis` |
| `/ui/elb` | Elastic Load Balancing | `DescribeLoadBalancers`, `DescribeTargetGroups` |
| `/ui/cloudmap` | AWS Cloud Map | `ListNamespaces`, `ListServices` |
| `/ui/codebuild` | AWS CodeBuild | `ListProjects`, `BatchGetProjects` |
| `/ui/amplify` | AWS Amplify | `ListApps` |
| `/ui/kinesis` | Amazon Kinesis Data Streams | `ListStreams`, `DescribeStreamSummary` |
| `/ui/glue` | AWS Glue | `GetDatabases`, `GetJobs` |
| `/ui/sns` | Amazon Simple Notification Service | `ListTopics`, `ListSubscriptions` |
| `/ui/sqs` | Amazon Simple Queue Service | `ListQueues`, `GetQueueAttributes` |
| `/ui/eventbridge` | Amazon EventBridge | `ListRules`, `ListEventBuses` |
| `/ui/scheduler` | Amazon EventBridge Scheduler | `ListSchedules`, `ListScheduleGroups` |
| `/ui/stepfunctions`, `/ui/stepfunctions/:stateMachineArn` | AWS Step Functions | `ListStateMachines`, `ListExecutions` |
| `/ui/cloudwatch` | Amazon CloudWatch | `DescribeAlarms`, `ListDashboards` |
| `/ui/logs`, `/ui/logs/:name` | Amazon CloudWatch Logs | `DescribeLogGroups`, `GetLogEvents` |
| `/ui/cloudtrail` | AWS CloudTrail | `DescribeTrails`, `LookupEvents` |
| `/ui/organizations`, `/ui/organizations/accounts/:accountId` | AWS Organizations | `ListAccounts`, `DescribeAccount` |
| `/ui/ssm` | AWS Systems Manager | `DescribeParameters`, `ListDocuments` |
| `/ui/iam`, `/ui/iam/users/:userName` | AWS Identity and Access Management | `ListUsers`, `ListAccessKeys` |
| `/ui/secretsmanager` | AWS Secrets Manager | `ListSecrets` |
| `/ui/kms` | AWS Key Management Service | `ListKeys`, `DescribeKey`, `ListAliases` |
| `/ui/acm` | AWS Certificate Manager | `ListCertificates` |
| `/ui/waf` | AWS WAF | `ListWebACLs`, `ListIPSets` |
| `/ui/budgets` | AWS Budgets | `GetCallerIdentity`, `DescribeBudgets` |
| `/ui/not-supported/:service` | — | none: the honest not-supported page |

## Embedding

`make embed` in `simulator-aws/` copies this package's `dist/` to
`simulator-aws/dist/` (see `make/go-app.mk`), which the binary bundles via
`//go:embed all:dist` (`simulator-aws/ui_embed.go`) and serves at `/ui/`. A
`-tags noui` build skips it.

## Development

- `bun run dev` — Vite dev server (`:5173`), proxying to a running simulator on `:4566`.
- `bun run build` — production bundle into `dist/`.
- `bun run preview` — serve the built bundle.
- `bun run test` — Vitest unit suite (`src/__tests__/`).
- `bun run test:e2e` — Playwright suite, including axe-core audits in both themes.
- `bun run typecheck` — `tsc --noEmit`.

The package `Makefile` wraps these as `make build` / `run` / `preview` / `test` / `lint` / `clean` (see `make/ui-app.mk`).

## See also

- [Workspace README](../../README.md) — dev-stack targets, ports, design system, error UX.
- [`@sockerless/ui-core`](../core/README.md) — shared components, hooks, tokens.
