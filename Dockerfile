# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.24.0
FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/job-agent ./cmd/job-agent && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/job-agent-migrate ./cmd/job-agent-migrate && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/job-agent-dashboard ./cmd/job-agent-dashboard

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build /out/ /usr/local/bin/
