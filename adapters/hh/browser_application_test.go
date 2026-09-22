package hh

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

func TestBrowserApplicationSubmitsPreflightedResumeAndCoverLetter(t *testing.T) {
	var gets atomic.Int32
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			gets.Add(1)
			if request.URL.Path != "/applicant/vacancy_response/popup" || request.URL.Query().Get("vacancyId") != "42" {
				t.Errorf("preflight URL = %s", request.URL.String())
			}
			writeBrowserPreflight(t, writer, `{
				"type":"modal",
				"responseStatus":{"letterMaxLength":2000,"resumes":{"17":{"id":17,"hash":"resume-hash","title":"Backend"}}},
				"countriesProfileVisibilityAgreement":{"show":false}
			}`)
		case http.MethodPost:
			posts.Add(1)
			if request.Header.Get("X-Xsrftoken") != "xsrf-value" {
				t.Errorf("X-Xsrftoken = %q", request.Header.Get("X-Xsrftoken"))
			}
			if err := request.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("parse multipart: %v", err)
			}
			want := map[string]string{
				"vacancy_id": "42", "resume_hash": "resume-hash", "letter": "Здравствуйте!",
				"ignore_postponed": "true", "incomplete": "false", "lux": "true", "withoutTest": "no",
				"mark_applicant_visible_in_vacancy_country": "false", "country_ids": "[]",
			}
			for key, expected := range want {
				if actual := request.FormValue(key); actual != expected {
					t.Errorf("field %s = %q, want %q", key, actual, expected)
				}
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"success":true,"topic_id":9001}`))
		default:
			t.Errorf("method = %s", request.Method)
		}
	}))
	defer server.Close()

	client := newTestBrowserApplicationClient(t, server, adapter.BrowserApplicationOptions{})
	result, err := client.SubmitApplication(context.Background(), adapter.ApplicationSubmitCommand{
		ProfileID: "primary", Vacancy: core.VacancyKey{Platform: Name, ExternalID: "42"}, ResumeID: "17",
		Message: "Здравствуйте!", IdempotencyKey: "application:primary:hh:42",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !result.Applied || result.AlreadyApplied || result.ExternalNegotiationID != "9001" {
		t.Fatalf("result = %#v", result)
	}
	if gets.Load() != 1 || posts.Load() != 1 {
		t.Fatalf("requests: GET=%d POST=%d", gets.Load(), posts.Load())
	}
}

func TestBrowserApplicationDoesNotPostWhenAlreadyApplied(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			posts.Add(1)
		}
		writeBrowserPreflight(t, writer, `{"type":"alreadyApplied","responseStatus":{"negotiations":{"topicId":"topic-7"}}}`)
	}))
	defer server.Close()
	client := newTestBrowserApplicationClient(t, server, adapter.BrowserApplicationOptions{})

	result, err := client.SubmitApplication(context.Background(), browserSubmitCommand())
	if err != nil || !result.Applied || !result.AlreadyApplied || result.ExternalNegotiationID != "topic-7" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if posts.Load() != 0 {
		t.Fatalf("POST count = %d", posts.Load())
	}
}

func TestBrowserApplicationStopsBeforePostForManualFlows(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		category core.ErrorCategory
		code     string
	}{
		{name: "questionnaire", payload: `{"type":"test-required"}`, category: core.ErrorValidationRequired, code: "questionnaire_required"},
		{name: "visibility", payload: `{"type":"modal","countriesProfileVisibilityAgreement":{"show":true}}`, category: core.ErrorConfirmationRequired, code: "resume_visibility_change_required"},
		{name: "unknown", payload: `{"type":"reload"}`, category: core.ErrorValidationRequired, code: "unsupported_response_flow"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method == http.MethodPost {
					posts.Add(1)
				}
				writeBrowserPreflight(t, writer, test.payload)
			}))
			defer server.Close()
			client := newTestBrowserApplicationClient(t, server, adapter.BrowserApplicationOptions{})

			_, err := client.SubmitApplication(context.Background(), browserSubmitCommand())
			if !core.ErrorIsCategory(err, test.category) {
				t.Fatalf("error = %v", err)
			}
			var operationError *core.OperationError
			if !errors.As(err, &operationError) || operationError.Metadata["code"] != test.code {
				t.Fatalf("operation error = %#v", operationError)
			}
			if posts.Load() != 0 {
				t.Fatalf("POST count = %d", posts.Load())
			}
		})
	}
}

func TestBrowserApplicationSuitableResumesExposeIDAndHash(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeBrowserPreflight(t, writer, `{
			"type":"modal",
			"responseStatus":{"resumes":{
				"17":{"id":17,"hash":"resume-hash","title":[{"string":"Backend"}]},
				"18":{"id":18,"hash":"unfinished","isIncomplete":true}
			}}
		}`)
	}))
	defer server.Close()
	client := newTestBrowserApplicationClient(t, server, adapter.BrowserApplicationOptions{})

	resumes, err := client.ListSuitableResumes(context.Background(), "primary", core.VacancyKey{Platform: Name, ExternalID: "42"})
	if err != nil {
		t.Fatalf("list resumes: %v", err)
	}
	seen := make(map[string]bool)
	for _, resume := range resumes {
		seen[resume.ID] = true
		if resume.ID == "17" && resume.Title != "Backend" {
			t.Fatalf("resume title = %q", resume.Title)
		}
	}
	if !seen["17"] || !seen["resume-hash"] || seen["18"] || seen["unfinished"] {
		t.Fatalf("resumes = %#v", resumes)
	}
	if len(resumes) != 2 || resumes[0].ID != "17" || resumes[1].ID != "resume-hash" {
		t.Fatalf("resumes are not deterministic: %#v", resumes)
	}
}

func TestBrowserApplicationSuitableResumesReadWrappedResponseStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeBrowserPreflight(t, writer, `{
			"type":"modal",
			"body":{"responseStatus":{"resumes":{
				"17":{"id":17,"hash":"resume-hash","title":[{"string":"Backend"}]}
			}}}
		}`)
	}))
	defer server.Close()
	client := newTestBrowserApplicationClient(t, server, adapter.BrowserApplicationOptions{})

	resumes, err := client.ListSuitableResumes(context.Background(), "primary", core.VacancyKey{Platform: Name, ExternalID: "42"})
	if err != nil {
		t.Fatalf("list resumes: %v", err)
	}
	seen := make(map[string]bool)
	for _, resume := range resumes {
		seen[resume.ID] = true
	}
	if !seen["17"] || !seen["resume-hash"] {
		t.Fatalf("resumes = %#v", resumes)
	}
}

func TestBrowserApplicationReconcilesUsedResumeWithoutPosting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Fatalf("unexpected method %s", request.Method)
		}
		writeBrowserPreflight(t, writer, `{
			"type":"modal",
			"responseStatus":{"usedResumeIds":[17],"resumes":{"17":{"id":17,"hash":"resume-hash"}},"negotiations":{"topic_id":77}}
		}`)
	}))
	defer server.Close()
	client := newTestBrowserApplicationClient(t, server, adapter.BrowserApplicationOptions{})

	result, err := client.ReconcileApplication(context.Background(), adapter.ApplicationReconcileCommand{
		ProfileID: "primary", Vacancy: core.VacancyKey{Platform: Name, ExternalID: "42"}, ResumeID: "resume-hash",
	})
	if err != nil || !result.Applied || result.ExternalNegotiationID != "77" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestBrowserApplicationTreatsServerFailureAfterPostAsAmbiguous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeBrowserPreflight(t, writer, `{"type":"modal","responseStatus":{"resumes":{"17":{"id":17,"hash":"resume-hash"}}}}`)
			return
		}
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := newTestBrowserApplicationClient(t, server, adapter.BrowserApplicationOptions{})

	_, err := client.SubmitApplication(context.Background(), browserSubmitCommand())
	if !core.ErrorIsCategory(err, core.ErrorAmbiguousResult) {
		t.Fatalf("error = %v", err)
	}
}

func newTestBrowserApplicationClient(t *testing.T, server *httptest.Server, options adapter.BrowserApplicationOptions) *BrowserApplicationClient {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	state := browserStorageState{Cookies: []browserCookie{
		{Name: "session", Value: "opaque", Domain: parsed.Hostname(), Path: "/"},
		{Name: "_xsrf", Value: "xsrf-value", Domain: parsed.Hostname(), Path: "/"},
	}}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(stateFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := NewBrowserApplicationClient("primary", stateFile, "JobAgent/Test", server.Client(), options)
	if err != nil {
		t.Fatal(err)
	}
	client.webBaseURL = server.URL
	client.reader.webBaseURL = server.URL
	return client
}

func TestBrowserApplicationFallsBackToPageStateWhenPopupHasNoResponseStatus(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/applicant/vacancy_response/popup":
			writeBrowserPreflight(t, writer, `{"type":"test-required"}`)
		case request.Method == http.MethodGet && request.URL.Path == "/applicant/vacancy_response":
			if request.URL.Query().Get("startedWithQuestion") != "false" {
				t.Errorf("page query = %v", request.URL.Query())
			}
			_, _ = writer.Write([]byte(vacancyTestPage(`{"vacancyResponsePopup":{"type":"alreadyApplied","vacancy":{"alreadyApplied":true,"negotiations":{"topicList":[{"id":"5570082491"}]}}}}`)))
		case request.Method == http.MethodPost:
			posts.Add(1)
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestBrowserApplicationClient(t, server, adapter.BrowserApplicationOptions{})

	result, err := client.SubmitApplication(context.Background(), browserSubmitCommand())
	if err != nil || !result.Applied || !result.AlreadyApplied || result.ExternalNegotiationID != "5570082491" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if posts.Load() != 0 {
		t.Fatalf("POST count = %d", posts.Load())
	}
}

func browserSubmitCommand() adapter.ApplicationSubmitCommand {
	return adapter.ApplicationSubmitCommand{
		ProfileID: "primary", Vacancy: core.VacancyKey{Platform: Name, ExternalID: "42"}, ResumeID: "17",
		Message: "Здравствуйте!", IdempotencyKey: "application:primary:hh:42",
	}
}

func writeBrowserPreflight(t *testing.T, writer http.ResponseWriter, payload string) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if _, err := writer.Write([]byte(payload)); err != nil {
		t.Errorf("write response: %v", err)
	}
}

func TestForbiddenBrowserApplicationStaysRetryable(t *testing.T) {
	_, err := classifyBrowserApplicationPOST(&http.Response{StatusCode: http.StatusForbidden}, []byte("<html>temporarily blocked</html>"))
	if !core.ErrorIsCategory(err, core.ErrorTemporaryFailure) {
		t.Fatalf("POST 403 category = %v", err)
	}
	if !strings.Contains(err.Error(), "temporarily blocked") {
		t.Fatalf("POST 403 lost the response body: %v", err)
	}
	response := &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("<html>denied</html>")),
	}
	if err := classifyBrowserApplicationGET(response); !core.ErrorIsCategory(err, core.ErrorTemporaryFailure) {
		t.Fatalf("GET 403 category = %v", err)
	}
}
