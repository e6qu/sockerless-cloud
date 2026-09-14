FROM oven/bun:1.3.10-alpine AS web
WORKDIR /src/ui
COPY ui/package.json ui/bun.lock ./
COPY ui/tsconfig.base.json ./
COPY ui/packages ./packages
RUN --mount=type=cache,target=/root/.bun/install/cache bun install --frozen-lockfile && cd packages/simulator-gcp && bun run build

FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY . .
COPY --from=web /src/ui/packages/simulator-gcp/dist /src/simulator-gcp/dist
WORKDIR /src/simulator-gcp
# The repository go.work lists the sdk/cli/terraform test modules, which
# .dockerignore keeps out of the image context. Replace it with a minimal
# workspace covering just this simulator and its support modules, so the
# build resolves the working tree's sim/realexec/ui-auth instead of failing on
# the absent test modules.
#
# The module and build caches are cache mounts, not part of the layer. Without
# them `go build` wrote both into this layer, and the publish workflows' cache
# export (mode=max) compressed and uploaded all of it on every run: 0.32.2's aws
# amd64 build spent 529 s of a 13-minute job exporting its cache, 418 s of that
# preparing layers, while the compile itself took 105 s — against the jobs'
# fifteen-minute ceiling. The layer now holds the source and the binary.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    rm /src/go.work /src/go.work.sum && \
    go work init . ../sim ../realexec ../ui-auth && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /simulator-gcp .

FROM alpine:3.20
RUN apk add --no-cache wget
COPY --from=builder /simulator-gcp /usr/local/bin/simulator-gcp
ENTRYPOINT ["simulator-gcp"]
