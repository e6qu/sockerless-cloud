# The build images come from mirror.gcr.io, Google's copy of Docker's official
# images, pinned to the index digest Docker Hub serves for the same tag: Docker
# Hub limits anonymous pulls per address, and shared CI runners exhaust it.
FROM mirror.gcr.io/library/golang:1.27-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS builder
WORKDIR /src
# The context carries the console bundle at simulator-gcp/dist, which the
# binary embeds with -tags console: build it first (the build-console action,
# or `make -C simulator-gcp embed` after building ui/).
COPY . .
WORKDIR /src/simulator-gcp
# The repository go.work lists the sdk/cli/terraform test modules, which
# .dockerignore keeps out of the image context. Replace it with a minimal
# workspace covering just this simulator and its support modules, so the
# build resolves the working tree's sim/ui-auth instead of failing on
# the absent test modules.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    rm /src/go.work /src/go.work.sum && \
    go work init . ../sim ../ui-auth && \
    CGO_ENABLED=0 go build -tags console -ldflags="-s -w" -o /simulator-gcp .

FROM mirror.gcr.io/library/alpine:3.24@sha256:3f4d6b32e26703c6082839234f43769d5046b3ac7c910033911d1e71d8cd630a
RUN apk add --no-cache wget
COPY --from=builder /simulator-gcp /usr/local/bin/simulator-gcp
ENTRYPOINT ["simulator-gcp"]
