module github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests

go 1.25.0

require (
	github.com/aws/aws-sdk-go-v2 v1.47.2
	github.com/aws/aws-sdk-go-v2/credentials v1.20.8
	github.com/aws/aws-sdk-go-v2/feature/rds/auth v1.7.5
	github.com/aws/aws-sdk-go-v2/service/ecr v1.66.3
	github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2 v1.63.3
	github.com/aws/aws-sdk-go-v2/service/iam v1.64.3
	github.com/aws/aws-sdk-go-v2/service/rds v1.130.2
	github.com/aws/aws-sdk-go-v2/service/s3 v1.114.2
	github.com/go-sql-driver/mysql v1.10.1
	github.com/jackc/pgx/v5 v5.11.0
	github.com/stretchr/testify v1.12.1
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.21 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.5 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.5 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.20 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.6 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.5 // indirect
	github.com/aws/smithy-go v1.28.4 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	golang.org/x/text v0.29.0 // indirect
)

require (
	github.com/e6qu/sockerless-cloud/testutil v0.1.0
	go.yaml.in/yaml/v3 v3.0.5 // indirect
)

replace github.com/e6qu/sockerless-cloud/testutil => ../../testutil
