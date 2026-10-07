# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY webui ./webui

ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN target_os="${TARGETOS:-$(go env GOOS)}" && \
    target_arch="${TARGETARCH:-$(go env GOARCH)}" && \
    CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/opencode2api ./cmd/opencode2api

FROM alpine:3.22

RUN apk add --no-cache ca-certificates

WORKDIR /app

COPY --from=build /out/opencode2api /usr/local/bin/opencode2api
COPY --chmod=0755 docker-entrypoint.sh /usr/local/bin/docker-entrypoint
COPY LICENSE /usr/share/licenses/opencode2api/LICENSE

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/docker-entrypoint"]
