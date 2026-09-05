module github.com/e6qu/sockerless-cloud/simulator-aws/sdk-tests

go 1.26.0

require (
	github.com/aws/aws-sdk-go-v2 v1.46.0
	github.com/aws/aws-sdk-go-v2/config v1.33.3
	github.com/aws/aws-sdk-go-v2/credentials v1.20.3
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.19.2
	github.com/aws/aws-sdk-go-v2/feature/rds/auth v1.7.2
	github.com/aws/aws-sdk-go-v2/service/acm v1.49.0
	github.com/aws/aws-sdk-go-v2/service/acmpca v1.55.0
	github.com/aws/aws-sdk-go-v2/service/amplify v1.47.0
	github.com/aws/aws-sdk-go-v2/service/apigateway v1.46.0
	github.com/aws/aws-sdk-go-v2/service/apigatewayv2 v1.41.0
	github.com/aws/aws-sdk-go-v2/service/applicationautoscaling v1.49.0
	github.com/aws/aws-sdk-go-v2/service/autoscaling v1.77.0
	github.com/aws/aws-sdk-go-v2/service/batch v1.74.0
	github.com/aws/aws-sdk-go-v2/service/budgets v1.50.0
	github.com/aws/aws-sdk-go-v2/service/cloudfront v1.72.0
	github.com/aws/aws-sdk-go-v2/service/cloudtrail v1.63.0
	github.com/aws/aws-sdk-go-v2/service/cloudwatch v1.71.0
	github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs v1.86.0
	github.com/aws/aws-sdk-go-v2/service/codebuild v1.77.0
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.67.0
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.329.0
	github.com/aws/aws-sdk-go-v2/service/ecr v1.64.0
	github.com/aws/aws-sdk-go-v2/service/ecs v1.96.0
	github.com/aws/aws-sdk-go-v2/service/efs v1.48.0
	github.com/aws/aws-sdk-go-v2/service/elasticache v1.60.0
	github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2 v1.62.0
	github.com/aws/aws-sdk-go-v2/service/eventbridge v1.53.0
	github.com/aws/aws-sdk-go-v2/service/firehose v1.50.0
	github.com/aws/aws-sdk-go-v2/service/glue v1.157.0
	github.com/aws/aws-sdk-go-v2/service/iam v1.63.0
	github.com/aws/aws-sdk-go-v2/service/kinesis v1.53.0
	github.com/aws/aws-sdk-go-v2/service/kms v1.59.0
	github.com/aws/aws-sdk-go-v2/service/lambda v1.107.0
	github.com/aws/aws-sdk-go-v2/service/organizations v1.59.0
	github.com/aws/aws-sdk-go-v2/service/rds v1.128.0
	github.com/aws/aws-sdk-go-v2/service/route53 v1.69.0
	github.com/aws/aws-sdk-go-v2/service/s3 v1.111.0
	github.com/aws/aws-sdk-go-v2/service/s3control v1.77.0
	github.com/aws/aws-sdk-go-v2/service/scheduler v1.24.0
	github.com/aws/aws-sdk-go-v2/service/secretsmanager v1.48.0
	github.com/aws/aws-sdk-go-v2/service/servicediscovery v1.48.0
	github.com/aws/aws-sdk-go-v2/service/sfn v1.49.0
	github.com/aws/aws-sdk-go-v2/service/sns v1.46.0
	github.com/aws/aws-sdk-go-v2/service/sqs v1.51.0
	github.com/aws/aws-sdk-go-v2/service/ssm v1.77.0
	github.com/aws/aws-sdk-go-v2/service/sts v1.49.0
	github.com/aws/aws-sdk-go-v2/service/wafv2 v1.82.0
	github.com/aws/smithy-go v1.28.1
	github.com/e6qu/sockerless-cloud/testutil v0.1.0
	github.com/go-jose/go-jose/v4 v4.1.5
	github.com/go-sql-driver/mysql v1.10.1
	github.com/golang/snappy v1.0.0
	github.com/google/go-containerregistry v0.22.1
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/jackc/pgx/v5 v5.10.0
	github.com/stretchr/testify v1.12.1
	golang.org/x/crypto v0.56.0
	golang.org/x/image v0.45.0
	golang.org/x/net v0.58.0
)

replace github.com/e6qu/sockerless-cloud/testutil => ../../testutil

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.20 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.2 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.2 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.19 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/endpoint-discovery v1.13.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.9.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.37.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.42.0 // indirect
	github.com/docker/cli v29.7.2+incompatible // indirect
	github.com/docker/docker-credential-helpers v0.9.8 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/sirupsen/logrus v1.10.2 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	gotest.tools/v3 v3.5.2 // indirect
)
