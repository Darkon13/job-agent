package hh

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

func publishCommand() adapter.ResumePublishCommand {
	return adapter.ResumePublishCommand{ProfileID: "primary", ResumeID: "resume-42", IdempotencyKey: "publish-1"}
}

func TestResumePublishPostsAndReturnsNoNextDate(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/resumes/resume-42/publish" {
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+testAccessToken {
			t.Error("missing bearer authorization")
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	result, err := client.PublishResume(context.Background(), publishCommand())
	if err != nil || result.NextPublishAt != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestResumePublishReadsNextPublishAt(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"next_publish_at":"2026-09-20T12:30:00+0300"}`))
	}))
	result, err := client.PublishResume(context.Background(), publishCommand())
	if err != nil || result.NextPublishAt == nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	want := time.Date(2026, 9, 20, 9, 30, 0, 0, time.UTC)
	if !result.NextPublishAt.Equal(want) {
		t.Fatalf("next publish at = %s, want %s", result.NextPublishAt, want)
	}
}

func TestResumePublishNormalizesEarlyPublishAsRateLimit(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusBadRequest)
		_, _ = response.Write([]byte(`{"next_publish_at":"2026-09-20T12:30:00+0300"}`))
	}))
	_, err := client.PublishResume(context.Background(), publishCommand())
	var operationError *core.OperationError
	if !errors.As(err, &operationError) || operationError.Category != core.ErrorRateLimited || operationError.RetryAfter == nil {
		t.Fatalf("error = %#v, want rate limit with retry_after", err)
	}
	if want := time.Date(2026, 9, 20, 9, 30, 0, 0, time.UTC); !operationError.RetryAfter.Equal(want) {
		t.Fatalf("retry_after = %s, want %s", operationError.RetryAfter, want)
	}
}

func TestResumePublishMapsTransportFailures(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		retry    string
		category core.ErrorCategory
	}{
		{name: "unauthorized", status: http.StatusForbidden, category: core.ErrorUnauthorized},
		{name: "rate limited", status: http.StatusTooManyRequests, retry: "120", category: core.ErrorRateLimited},
		{name: "missing", status: http.StatusNotFound, category: core.ErrorPermanentFailure},
		{name: "server error", status: http.StatusBadGateway, category: core.ErrorTemporaryFailure},
		{name: "rejected", status: http.StatusBadRequest, category: core.ErrorPermanentFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				if test.retry != "" {
					response.Header().Set("Retry-After", test.retry)
				}
				response.WriteHeader(test.status)
			}))
			_, err := client.PublishResume(context.Background(), publishCommand())
			var operationError *core.OperationError
			if !errors.As(err, &operationError) || operationError.Category != test.category {
				t.Fatalf("error = %#v, want category %s", err, test.category)
			}
			if test.retry != "" && operationError.RetryAfter == nil {
				t.Fatal("expected retry_after from header")
			}
		})
	}
}

func TestResumePublishRejectsInvalidCommands(t *testing.T) {
	client := newReadClientFixture(t, http.NotFoundHandler())
	if _, err := client.PublishResume(context.Background(), adapter.ResumePublishCommand{ProfileID: "primary"}); err == nil {
		t.Fatal("expected empty resume id to fail")
	}
	if _, err := client.PublishResume(context.Background(), adapter.ResumePublishCommand{ProfileID: "secondary", ResumeID: "resume-42"}); err == nil {
		t.Fatal("expected profile mismatch to fail")
	}
}
