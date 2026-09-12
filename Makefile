VERSION := $(shell tr -d '\n' < buildinfo/VERSION)
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || printf unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
MODIFIED ?= $(shell test -z "$$(git status --porcelain 2>/dev/null)" && printf false || printf true)
LDFLAGS := -s -w -X github.com/Darkon13/job-agent/buildinfo.Version=$(VERSION) -X github.com/Darkon13/job-agent/buildinfo.Commit=$(COMMIT) -X github.com/Darkon13/job-agent/buildinfo.BuildTime=$(BUILD_TIME) -X github.com/Darkon13/job-agent/buildinfo.Modified=$(MODIFIED)

.PHONY: version build verify smoke release-check

version:
	@go run ./cmd/job-agent --version

build:
	@mkdir -p dist
	CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="$(LDFLAGS)" -o dist/job-agent ./cmd/job-agent
	CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="$(LDFLAGS)" -o dist/job-agent-migrate ./cmd/job-agent-migrate
	CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="$(LDFLAGS)" -o dist/job-agent-dashboard ./cmd/job-agent-dashboard

verify:
	go test -race ./... -count=1
	go vet ./...
	git diff --check

smoke:
	@scripts/smoke-runtime.sh

release-check: verify
	@case "$(VERSION)" in *-dev) echo "release version must not end with -dev: $(VERSION)"; exit 1;; esac
	@test -z "$$(git status --porcelain)" || { echo "release requires a clean worktree"; exit 1; }
	@test "$$(git tag --list v$(VERSION))" = "" || { echo "tag v$(VERSION) already exists"; exit 1; }
