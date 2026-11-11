module github.com/e6qu/sockerless-cloud/simulator-aws/sdk-tests

go 1.26.0

require (
	github.com/aws/aws-sdk-go-v2 v1.48.0
	github.com/aws/aws-sdk-go-v2/config v1.33.18
	github.com/aws/aws-sdk-go-v2/credentials v1.21.0
	github.com/aws/aws-sdk-go-v2/feature/rds/auth v1.7.12
	github.com/aws/aws-sdk-go-v2/service/acm v1.51.5
	github.com/aws/aws-sdk-go-v2/service/acmpca v1.56.12
	github.com/aws/aws-sdk-go-v2/service/amplify v1.49.7
	github.com/aws/aws-sdk-go-v2/service/apigateway v1.52.9
	github.com/aws/aws-sdk-go-v2/service/apigatewayv2 v1.46.9
	github.com/aws/aws-sdk-go-v2/service/applicationautoscaling v1.51.12
	github.com/aws/aws-sdk-go-v2/service/autoscaling v1.81.0
	github.com/aws/aws-sdk-go-v2/service/batch v1.79.1
	github.com/aws/aws-sdk-go-v2/service/budgets v1.53.9
	github.com/aws/aws-sdk-go-v2/service/cloudfront v1.75.6
	github.com/aws/aws-sdk-go-v2/service/cloudtrail v1.65.12
	github.com/aws/aws-sdk-go-v2/service/cloudwatch v1.75.7
	github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs v1.92.2
	github.com/aws/aws-sdk-go-v2/service/codebuild v1.78.12
	github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider v1.75.11
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.74.0
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.344.3
	github.com/aws/aws-sdk-go-v2/service/ecr v1.67.6
	github.com/aws/aws-sdk-go-v2/service/ecs v1.105.1
	github.com/aws/aws-sdk-go-v2/service/efs v1.51.6
	github.com/aws/aws-sdk-go-v2/service/elasticache v1.65.0
	github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2 v1.64.0
	github.com/aws/aws-sdk-go-v2/service/eventbridge v1.55.11
	github.com/aws/aws-sdk-go-v2/service/firehose v1.52.12
	github.com/aws/aws-sdk-go-v2/service/glue v1.175.1
	github.com/aws/aws-sdk-go-v2/service/iam v1.65.0
	github.com/aws/aws-sdk-go-v2/service/kinesis v1.56.13
	github.com/aws/aws-sdk-go-v2/service/kms v1.62.6
	github.com/aws/aws-sdk-go-v2/service/lambda v1.114.4
	github.com/aws/aws-sdk-go-v2/service/organizations v1.62.8
	github.com/aws/aws-sdk-go-v2/service/pi v1.46.9
	github.com/aws/aws-sdk-go-v2/service/rds v1.133.0
	github.com/aws/aws-sdk-go-v2/service/route53 v1.70.10
	github.com/aws/aws-sdk-go-v2/service/s3 v1.114.10
	github.com/aws/aws-sdk-go-v2/service/s3control v1.80.1
	github.com/aws/aws-sdk-go-v2/service/scheduler v1.26.5
	github.com/aws/aws-sdk-go-v2/service/secretsmanager v1.50.12
	github.com/aws/aws-sdk-go-v2/service/servicediscovery v1.49.12
	github.com/aws/aws-sdk-go-v2/service/servicequotas v1.43.12
	github.com/aws/aws-sdk-go-v2/service/sfn v1.53.0
	github.com/aws/aws-sdk-go-v2/service/sns v1.48.0
	github.com/aws/aws-sdk-go-v2/service/sqs v1.53.0
	github.com/aws/aws-sdk-go-v2/service/ssm v1.82.1
	github.com/aws/aws-sdk-go-v2/service/sts v1.52.0
	github.com/aws/aws-sdk-go-v2/service/wafv2 v1.85.2
	github.com/aws/smithy-go v1.28.9
	github.com/e6qu/sockerless-cloud/testutil v0.1.0
	github.com/go-jose/go-jose/v4 v4.1.9
	github.com/go-sql-driver/mysql v1.10.1
	github.com/golang/snappy v1.0.0
	github.com/google/go-containerregistry v0.22.1
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/jackc/pgx/v5 v5.12.0
	github.com/redis/go-redis/v9 v9.23.0
	github.com/stretchr/testify v1.12.1
	golang.org/x/crypto v0.58.0
	golang.org/x/image v0.48.0
	golang.org/x/net v0.62.0
)

replace github.com/e6qu/sockerless-cloud/testutil => ../../testutil

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.27 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.20.9 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.12 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.12 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.25 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/endpoint-discovery v1.13.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.13.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.39.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.44.5 // indirect
	github.com/beevik/etree v1.8.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/docker/cli v29.7.2+incompatible // indirect
	github.com/docker/docker-credential-helpers v0.9.8 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jonboulle/clockwork v0.5.0 // indirect
	github.com/klauspost/compress v1.20.1 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/russellhaering/goxmldsig v1.6.1 // indirect
	github.com/sirupsen/logrus v1.10.2 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	gotest.tools/v3 v3.5.2 // indirect
)
