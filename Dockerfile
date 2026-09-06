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

FROM gcr.io/distroless/static-debian12:nonroot AS job-agent
COPY --from=build /out/job-agent /job-agent
ENTRYPOINT ["/job-agent"]

FROM gcr.io/distroless/static-debian12:nonroot AS job-agent-migrate
COPY --from=build /out/job-agent-migrate /job-agent-migrate
ENTRYPOINT ["/job-agent-migrate"]

FROM gcr.io/distroless/static-debian12:nonroot AS job-agent-dashboard
COPY --from=build /out/job-agent-dashboard /job-agent-dashboard
ENTRYPOINT ["/job-agent-dashboard"]
