# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.26.0
FROM golang:${GO_VERSION}-alpine AS build
ARG VERSION=1.9.11
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
ARG MODIFIED=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    LDFLAGS="-s -w -X github.com/Darkon13/job-agent/buildinfo.Version=${VERSION} -X github.com/Darkon13/job-agent/buildinfo.Commit=${COMMIT} -X github.com/Darkon13/job-agent/buildinfo.BuildTime=${BUILD_TIME} -X github.com/Darkon13/job-agent/buildinfo.Modified=${MODIFIED}" && \
    CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="$LDFLAGS" -o /out/job-agent ./cmd/job-agent && \
    CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="$LDFLAGS" -o /out/job-agent-migrate ./cmd/job-agent-migrate && \
    CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="$LDFLAGS" -o /out/job-agent-dashboard ./cmd/job-agent-dashboard

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
ARG VERSION=1.9.11
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
WORKDIR /
LABEL org.opencontainers.image.title="Job Agent" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_TIME}" \
      org.opencontainers.image.source="https://github.com/Darkon13/job-agent"
COPY --from=build /out/ /usr/local/bin/
