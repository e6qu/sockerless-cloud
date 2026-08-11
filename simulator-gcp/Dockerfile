FROM oven/bun:1.3.10-alpine AS web
WORKDIR /src/ui
COPY ui/package.json ui/bun.lock ./
COPY ui/tsconfig.base.json ./
COPY ui/packages ./packages
RUN --mount=type=cache,target=/root/.bun/install/cache bun install --frozen-lockfile && cd packages/simulator-gcp && bun run build

FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY . .
COPY --from=web /src/ui/packages/simulator-gcp/dist /src/simulator-gcp/dist
WORKDIR /src/simulator-gcp
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /simulator-gcp .

FROM alpine:3.20
RUN apk add --no-cache wget
COPY --from=builder /simulator-gcp /usr/local/bin/simulator-gcp
ENTRYPOINT ["simulator-gcp"]
