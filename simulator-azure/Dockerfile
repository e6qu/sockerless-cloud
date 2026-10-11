# The build images come from the Amazon ECR Public copy of Docker's official
# images, pinned to the index digest Docker Hub serves for the same tag: Docker
# Hub limits anonymous pulls per address, and shared CI runners exhaust it.
FROM public.ecr.aws/docker/library/golang:1.27-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS builder
WORKDIR /src
# The context carries the committed console bundle, simulator-azure/dist, which
# the binary embeds: the image serves the same console as `go install` and the
# release binaries.
COPY . .
WORKDIR /src/simulator-azure
# The repository go.work lists the sdk/cli/terraform test modules, which
# .dockerignore keeps out of the image context. Replace it with a minimal
# workspace covering just this simulator and its support modules, so the
# build resolves the working tree's sim/realexec/ui-auth instead of failing on
# the absent test modules.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    rm /src/go.work /src/go.work.sum && \
    go work init . ../sim ../realexec ../ui-auth && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /simulator-azure .

FROM public.ecr.aws/docker/library/alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache wget
COPY --from=builder /simulator-azure /usr/local/bin/simulator-azure
ENTRYPOINT ["simulator-azure"]
