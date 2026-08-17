module github.com/e6qu/sockerless-cloud/simulator-aws/sdk-tests

go 1.25.0

require (
	github.com/aws/aws-sdk-go-v2 v1.43.6
	github.com/aws/aws-sdk-go-v2/config v1.32.37
	github.com/aws/aws-sdk-go-v2/credentials v1.19.36
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.37
	github.com/aws/aws-sdk-go-v2/feature/rds/auth v1.6.37
	github.com/aws/aws-sdk-go-v2/service/acm v1.44.1
	github.com/aws/aws-sdk-go-v2/service/acmpca v1.50.2
	github.com/aws/aws-sdk-go-v2/service/amplify v1.41.6
	github.com/aws/aws-sdk-go-v2/service/apigateway v1.42.6
	github.com/aws/aws-sdk-go-v2/service/apigatewayv2 v1.37.6
	github.com/aws/aws-sdk-go-v2/service/applicationautoscaling v1.45.6
	github.com/aws/aws-sdk-go-v2/service/autoscaling v1.72.1
	github.com/aws/aws-sdk-go-v2/service/batch v1.68.6
	github.com/aws/aws-sdk-go-v2/service/budgets v1.46.6
	github.com/aws/aws-sdk-go-v2/service/cloudfront v1.67.6
	github.com/aws/aws-sdk-go-v2/service/cloudtrail v1.58.6
	github.com/aws/aws-sdk-go-v2/service/cloudwatch v1.66.5
	github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs v1.82.2
	github.com/aws/aws-sdk-go-v2/service/codebuild v1.72.6
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.63.3
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.321.2
	github.com/aws/aws-sdk-go-v2/service/ecr v1.60.6
	github.com/aws/aws-sdk-go-v2/service/ecs v1.90.2
	github.com/aws/aws-sdk-go-v2/service/efs v1.44.6
	github.com/aws/aws-sdk-go-v2/service/elasticache v1.56.6
	github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2 v1.58.7
	github.com/aws/aws-sdk-go-v2/service/eventbridge v1.48.6
	github.com/aws/aws-sdk-go-v2/service/firehose v1.46.6
	github.com/aws/aws-sdk-go-v2/service/glue v1.153.0
	github.com/aws/aws-sdk-go-v2/service/iam v1.59.1
	github.com/aws/aws-sdk-go-v2/service/kinesis v1.46.6
	github.com/aws/aws-sdk-go-v2/service/kms v1.55.6
	github.com/aws/aws-sdk-go-v2/service/lambda v1.101.4
	github.com/aws/aws-sdk-go-v2/service/organizations v1.53.8
	github.com/aws/aws-sdk-go-v2/service/rds v1.124.3
	github.com/aws/aws-sdk-go-v2/service/route53 v1.65.8
	github.com/aws/aws-sdk-go-v2/service/s3 v1.107.2
	github.com/aws/aws-sdk-go-v2/service/scheduler v1.20.6
	github.com/aws/aws-sdk-go-v2/service/secretsmanager v1.44.6
	github.com/aws/aws-sdk-go-v2/service/servicediscovery v1.43.6
	github.com/aws/aws-sdk-go-v2/service/sfn v1.45.6
	github.com/aws/aws-sdk-go-v2/service/sns v1.42.6
	github.com/aws/aws-sdk-go-v2/service/sqs v1.46.6
	github.com/aws/aws-sdk-go-v2/service/ssm v1.73.6
	github.com/aws/aws-sdk-go-v2/service/sts v1.45.6
	github.com/aws/aws-sdk-go-v2/service/wafv2 v1.77.5
	github.com/aws/smithy-go v1.27.8
	github.com/e6qu/sockerless-cloud/testutil v0.1.0
	github.com/go-jose/go-jose/v4 v4.1.4
	github.com/go-sql-driver/mysql v1.10.0
	github.com/golang/snappy v1.0.0
	github.com/google/go-containerregistry v0.21.9
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/jackc/pgx/v5 v5.10.0
	github.com/stretchr/testify v1.12.0
	golang.org/x/crypto v0.55.0
	golang.org/x/image v0.45.0
	golang.org/x/net v0.58.0
)

replace github.com/e6qu/sockerless-cloud/testutil => ../../testutil

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.18 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.37 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.37 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.38 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.17 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.9.30 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/endpoint-discovery v1.12.14 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.37 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.19.38 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.5.6 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.33.6 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.38.6 // indirect
	github.com/docker/cli v29.6.2+incompatible // indirect
	github.com/docker/docker-credential-helpers v0.9.3 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/klauspost/compress v1.19.1 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/sirupsen/logrus v1.9.4 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	gotest.tools/v3 v3.5.2 // indirect
)
