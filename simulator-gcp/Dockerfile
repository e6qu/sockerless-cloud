FROM golang:1.26-alpine AS builder
WORKDIR /src
# The context carries the committed console bundle, simulator-gcp/dist, which
# the binary embeds: the image serves the same console as `go install` and the
# release binaries.
COPY . .
WORKDIR /src/simulator-gcp
# The repository go.work lists the sdk/cli/terraform test modules, which
# .dockerignore keeps out of the image context. Replace it with a minimal
# workspace covering just this simulator and its support modules, so the
# build resolves the working tree's sim/realexec/ui-auth instead of failing on
# the absent test modules.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    rm /src/go.work /src/go.work.sum && \
    go work init . ../sim ../realexec ../ui-auth && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /simulator-gcp .

FROM alpine:3.20
RUN apk add --no-cache wget
COPY --from=builder /simulator-gcp /usr/local/bin/simulator-gcp
ENTRYPOINT ["simulator-gcp"]
