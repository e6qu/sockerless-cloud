module github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests

go 1.25.0

require (
	github.com/aws/aws-sdk-go-v2 v1.47.1
	github.com/aws/aws-sdk-go-v2/credentials v1.20.6
	github.com/aws/aws-sdk-go-v2/service/rds v1.130.0
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.4 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.19 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.4 // indirect
	github.com/aws/smithy-go v1.28.2 // indirect
)

require (
	github.com/e6qu/sockerless-cloud/testutil v0.1.0
	go.yaml.in/yaml/v3 v3.0.5 // indirect
)

replace github.com/e6qu/sockerless-cloud/testutil => ../../testutil
