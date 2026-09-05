package hh

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Darkon13/job-agent/core"
)

func TestListSuitableResumesReadsEveryPageAndDeduplicates(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/vacancies/42/suitable_resumes" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+testAccessToken {
			t.Error("missing bearer authorization")
		}
		switch request.URL.Query().Get("page") {
		case "0":
			_, _ = response.Write([]byte(`{"items":[{"id":"resume-1","title":"Backend"}],"page":0,"pages":2}`))
		case "1":
			_, _ = response.Write([]byte(`{"items":[{"id":"resume-1","title":"Backend"},{"id":"resume-2","title":"Go"}],"page":1,"pages":2}`))
		default:
			t.Errorf("unexpected page %q", request.URL.Query().Get("page"))
		}
	}))

	resumes, err := client.ListSuitableResumes(context.Background(), "primary", core.VacancyKey{Platform: Name, ExternalID: "42"})
	if err != nil {
		t.Fatalf("list suitable resumes: %v", err)
	}
	if len(resumes) != 2 || resumes[0].ID != "resume-1" || resumes[0].Title != "Backend" || resumes[1].ID != "resume-2" {
		t.Fatalf("resumes = %#v", resumes)
	}
}

func TestListSuitableResumesNormalizesFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		category core.ErrorCategory
	}{
		{name: "forbidden", status: http.StatusForbidden, category: core.ErrorUnauthorized},
		{name: "rate limited", status: http.StatusTooManyRequests, category: core.ErrorRateLimited},
		{name: "server", status: http.StatusServiceUnavailable, category: core.ErrorTemporaryFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(test.status)
			}))
			_, err := client.ListSuitableResumes(context.Background(), "primary", core.VacancyKey{Platform: Name, ExternalID: "42"})
			var operationError *core.OperationError
			if !errors.As(err, &operationError) || operationError.Category != test.category {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}
