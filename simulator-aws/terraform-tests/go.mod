module github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests

go 1.26.0

require (
	github.com/aws/aws-sdk-go-v2 v1.47.7
	github.com/aws/aws-sdk-go-v2/credentials v1.20.15
	github.com/aws/aws-sdk-go-v2/feature/rds/auth v1.7.10
	github.com/aws/aws-sdk-go-v2/service/ecr v1.67.4
	github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2 v1.63.8
	github.com/aws/aws-sdk-go-v2/service/iam v1.64.8
	github.com/aws/aws-sdk-go-v2/service/organizations v1.62.6
	github.com/aws/aws-sdk-go-v2/service/rds v1.132.1
	github.com/aws/aws-sdk-go-v2/service/s3 v1.114.8
	github.com/aws/aws-sdk-go-v2/service/sts v1.51.8
	github.com/aws/smithy-go v1.28.8
	github.com/go-sql-driver/mysql v1.10.1
	github.com/jackc/pgx/v5 v5.12.0
	github.com/redis/go-redis/v9 v9.23.0
	github.com/stretchr/testify v1.12.1
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.26 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.10 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.10 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.10 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.24 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.11 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.10 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.10 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

require (
	github.com/e6qu/sockerless-cloud/testutil v0.1.0
	go.yaml.in/yaml/v3 v3.0.5 // indirect
)

replace github.com/e6qu/sockerless-cloud/testutil => ../../testutil
