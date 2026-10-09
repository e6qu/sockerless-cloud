module github.com/e6qu/sockerless-cloud/simulator-gcp

go 1.26.3

require (
	cel.dev/cel-go v0.32.0
	cloud.google.com/go/apigateway v1.16.0
	cloud.google.com/go/artifactregistry v1.27.0
	cloud.google.com/go/bigtable v1.58.0
	cloud.google.com/go/cloudbuild v1.34.0
	cloud.google.com/go/eventarc v1.26.0
	cloud.google.com/go/firestore v1.26.0
	cloud.google.com/go/functions v1.26.0
	cloud.google.com/go/iam v1.14.0
	cloud.google.com/go/kms v1.35.0
	cloud.google.com/go/logging v1.20.0
	cloud.google.com/go/longrunning v1.3.0
	cloud.google.com/go/pubsub v1.51.1
	cloud.google.com/go/redis v1.26.0
	cloud.google.com/go/resourcemanager v1.17.0
	cloud.google.com/go/run v1.23.0
	cloud.google.com/go/secretmanager v1.22.0
	cloud.google.com/go/serviceusage v1.16.0
	cloud.google.com/go/spanner v1.96.0
	cloud.google.com/go/vpcaccess v1.15.0
	github.com/e6qu/sockerless-cloud/realexec v0.0.0-20261003021821-79bf1cd9c7da
	github.com/e6qu/sockerless-cloud/sim v0.0.0-20261006234659-47a14bb9d0df
	github.com/e6qu/sockerless-cloud/ui-auth v0.0.0-20260912152828-8fd99b4320ff
	github.com/hamba/avro/v2 v2.31.0
	github.com/klauspost/compress v1.20.1
	github.com/moby/moby/api v1.56.1
	github.com/moby/moby/client v0.6.1
	github.com/parquet-go/parquet-go v0.32.0
	github.com/ulikunitz/xz v0.5.17
	go.yaml.in/yaml/v3 v3.0.5
	golang.org/x/sys v0.48.0
	google.golang.org/genproto v0.0.0-20260928230214-8a89bd6388cc
	google.golang.org/genproto/googleapis/api v0.0.0-20260928230214-8a89bd6388cc
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260928230214-8a89bd6388cc
	google.golang.org/grpc v1.84.0
	google.golang.org/protobuf v1.36.12
	modernc.org/sqlite v1.60.1
)

require (
	cel.dev/expr v0.25.3 // indirect
	cloud.google.com/go/pubsub/v2 v2.7.0 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/andybalholm/brotli v1.2.6 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/containerd/errdefs v1.0.0 // indirect
	github.com/containerd/errdefs/pkg v0.3.0 // indirect
	github.com/coreos/go-oidc/v3 v3.21.0 // indirect
	github.com/distribution/reference v0.6.0 // indirect
	github.com/docker/go-connections v0.8.1 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/go-jose/go-jose/v4 v4.1.5 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/golang/snappy v1.0.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.31.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/moby/docker-image-spec v1.3.1 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/parquet-go/bitpack v1.1.0 // indirect
	github.com/parquet-go/jsonlite v1.5.5 // indirect
	github.com/pierrec/lz4/v4 v4.1.32 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rs/zerolog v1.35.1 // indirect
	github.com/twpayne/go-geom v1.7.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.72.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/runtime v0.72.0 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp v0.23.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk/log v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.opentelemetry.io/proto/otlp v1.11.1 // indirect
	golang.org/x/exp v0.0.0-20260908205506-85c1c2202aba // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/oauth2 v0.37.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	modernc.org/libc v1.77.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)
