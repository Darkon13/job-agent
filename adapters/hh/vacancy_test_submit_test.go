package hh

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

const openVacancyTestState = `{"vacancyResponsePopup":{"vacancy":{"test":{"hasTests":true,"testId":"10","required":true}}},
	"vacancyTests":{"10":{"uidPk":123,"guid":"guid-1","description":"Go basics","required":true,"startTime":1750000000,
		"tasks":[
			{"id":"1","description":"Explain experience","open":true},
			{"id":"2","description":"Describe a project","open":true}]}}}`

func vacancyTestPopupPage(state, xsrf string) string {
	return `<html><body><form><input type="hidden" name="_xsrf" value="` + xsrf + `"/></form>` +
		`<template data-name="VacancyResponsePopup-InitialState">` + state + `</template></body></html>`
}

func requireOperationCategory(t *testing.T, err error, category core.ErrorCategory) {
	t.Helper()
	var operationErr *core.OperationError
	if !errors.As(err, &operationErr) || operationErr.Category != category {
		t.Fatalf("error = %v, want category %s", err, category)
	}
}

func TestBrowserSubmitVacancyTestFillsOpenTextForm(t *testing.T) {
	var posts atomic.Int32
	var submittedFlag atomic.Bool
	var submitted url.Values
	var submittedType string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/applicant/vacancy_response" {
			t.Errorf("path = %q", request.URL.Path)
		}
		switch request.Method {
		case http.MethodGet:
			if submittedFlag.Load() {
				_, _ = writer.Write([]byte(`<html><body>application form</body></html>`))
				return
			}
			_, _ = writer.Write([]byte(vacancyTestPopupPage(openVacancyTestState, "xsrf-field")))
		case http.MethodPost:
			posts.Add(1)
			submittedType = request.Header.Get("Content-Type")
			if err := request.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
			}
			submitted = request.PostForm
			submittedFlag.Store(true)
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{}`))
		default:
			t.Errorf("method = %s", request.Method)
		}
	}))
	defer server.Close()

	client := newTestBrowserApplicationClient(t, server, adapter.BrowserApplicationOptions{})
	err := client.SubmitVacancyTest(context.Background(), "primary", core.VacancyKey{Platform: Name, ExternalID: "42"}, []core.ResolvedAnswer{
		{QuestionID: "1", Text: "Five years of Go"},
		{QuestionID: "2", Text: "A local job agent"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("posts = %d", posts.Load())
	}
	if submittedType != "application/x-www-form-urlencoded" {
		t.Fatalf("content type = %q", submittedType)
	}
	want := map[string]string{
		"_xsrf": "xsrf-field", "uidPk": "123", "guid": "guid-1",
		"startTime": "1750000000", "testRequired": "true",
		"task_1_text": "Five years of Go", "task_2_text": "A local job agent",
	}
	for key, expected := range want {
		if actual := submitted.Get(key); actual != expected {
			t.Errorf("field %s = %q, want %q", key, actual, expected)
		}
	}
}

func TestBrowserSubmitVacancyTestRejectsUnsupportedTasks(t *testing.T) {
	state := `{"vacancyResponsePopup":{"vacancy":{"test":{"hasTests":true,"testId":"10"}}},
		"vacancyTests":{"10":{"uidPk":1,"guid":"g","tasks":[
			{"id":"1","description":"Pick one","candidateSolutions":[{"id":10,"description":"Go"}]}]}}}`
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			posts.Add(1)
		}
		_, _ = writer.Write([]byte(vacancyTestPopupPage(state, "xsrf")))
	}))
	defer server.Close()

	client := newTestBrowserApplicationClient(t, server, adapter.BrowserApplicationOptions{})
	err := client.SubmitVacancyTest(context.Background(), "primary", core.VacancyKey{Platform: Name, ExternalID: "42"}, []core.ResolvedAnswer{
		{QuestionID: "1", SelectedOptionIDs: []string{"10"}},
	})
	requireOperationCategory(t, err, core.ErrorUnsupported)
	if posts.Load() != 0 {
		t.Fatalf("posts = %d", posts.Load())
	}
}

func TestBrowserSubmitVacancyTestRequiresEveryAnswer(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			posts.Add(1)
		}
		_, _ = writer.Write([]byte(vacancyTestPopupPage(openVacancyTestState, "xsrf")))
	}))
	defer server.Close()

	client := newTestBrowserApplicationClient(t, server, adapter.BrowserApplicationOptions{})
	err := client.SubmitVacancyTest(context.Background(), "primary", core.VacancyKey{Platform: Name, ExternalID: "42"}, []core.ResolvedAnswer{
		{QuestionID: "1", Text: "Only the first"},
	})
	requireOperationCategory(t, err, core.ErrorValidationRequired)
	if posts.Load() != 0 {
		t.Fatalf("posts = %d", posts.Load())
	}
}

func TestBrowserSubmitVacancyTestRequiresConfirmation(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			posts.Add(1)
		}
		_, _ = writer.Write([]byte(vacancyTestPopupPage(openVacancyTestState, "xsrf")))
	}))
	defer server.Close()

	client := newTestBrowserApplicationClient(t, server, adapter.BrowserApplicationOptions{})
	err := client.SubmitVacancyTest(context.Background(), "primary", core.VacancyKey{Platform: Name, ExternalID: "42"}, []core.ResolvedAnswer{
		{QuestionID: "1", Text: "Five years of Go"},
		{QuestionID: "2", Text: "A local job agent"},
	})
	requireOperationCategory(t, err, core.ErrorValidationRequired)
	if posts.Load() != 1 {
		t.Fatalf("posts = %d", posts.Load())
	}
}
