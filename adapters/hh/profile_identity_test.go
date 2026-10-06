package hh

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestReadProfileIdentityCapturesNameAndResumes(t *testing.T) {
	client := newBrowserReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/applicant/profile/me":
			state := map[string]any{
				"applicantId": 123456,
				"applicantResumes": []any{
					map[string]any{"_attributes": map[string]any{"id": "42", "hash": "resume-hash", "title": "Go developer"}},
					map[string]any{"_attributes": map[string]any{"id": "43", "hash": "resume-hash-2", "name": "Backend"}},
				},
			}
			encoded, _ := json.Marshal(state)
			_, _ = response.Write([]byte(`<template class="ResumeProfileFront-InitialState">` + string(encoded) + `</template>`))
		case request.URL.Path == "/applicant/resume":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"resume": map[string]any{"email": []any{map[string]any{"string": "user@example.test"}}},
			})
		case request.URL.Path == "/shards/applicant/profile/get_full_data":
			_ = json.NewEncoder(response).Encode(map[string]any{"profile": map[string]any{"fields": map[string]any{
				"firstName":            []any{map[string]any{"string": "Антон"}},
				"lastName":             []any{map[string]any{"string": "Шумаков"}},
				"communicationMethods": []any{map[string]any{"type": "phone", "value": "+79991234567"}},
			}}})
		default:
			t.Errorf("unexpected request %s", request.URL.String())
			http.NotFound(response, request)
		}
	}))
	snapshot, err := client.ReadProfileIdentity(context.Background(), "primary")
	if err != nil {
		t.Fatalf("read identity: %v", err)
	}
	if len(snapshot.Resumes) != 2 || snapshot.Resumes[0].ID != "42" || snapshot.Resumes[0].Title != "Go developer" ||
		snapshot.Resumes[1].ID != "43" || snapshot.Resumes[1].Title != "Backend" {
		t.Fatalf("resumes = %#v", snapshot.Resumes)
	}
	if snapshot.DisplayName != "Антон Шумаков" {
		t.Fatalf("display name = %q", snapshot.DisplayName)
	}
	if snapshot.Email != "u***@example.test" {
		t.Fatalf("email = %q", snapshot.Email)
	}
	if snapshot.Phone != "79*******67" {
		t.Fatalf("phone = %q", snapshot.Phone)
	}
	if !strings.HasPrefix(snapshot.AccountHash, "sha256:") || len(snapshot.AccountHash) != len("sha256:")+12 {
		t.Fatalf("account hash = %q", snapshot.AccountHash)
	}
	if snapshot.CapturedAt.IsZero() {
		t.Fatal("capture time is missing")
	}
}

func TestReadProfileIdentityKeepsResumesWithoutContacts(t *testing.T) {
	client := newBrowserReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/applicant/profile/me":
			state := map[string]any{"applicantResumes": []any{map[string]any{"_attributes": map[string]any{"id": "42"}}}}
			encoded, _ := json.Marshal(state)
			_, _ = response.Write([]byte(`<template class="ResumeProfileFront-InitialState">` + string(encoded) + `</template>`))
		default:
			http.Error(response, "unauthorized", http.StatusUnauthorized)
		}
	}))
	snapshot, err := client.ReadProfileIdentity(context.Background(), "primary")
	if err != nil {
		t.Fatalf("read identity: %v", err)
	}
	if len(snapshot.Resumes) != 1 || snapshot.DisplayName != "" || snapshot.Email != "" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestReadProfileIdentityRejectsAnotherProfile(t *testing.T) {
	client := newBrowserReadClientFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("unexpected request")
	}))
	if _, err := client.ReadProfileIdentity(context.Background(), "secondary"); err == nil {
		t.Fatal("expected a mismatched profile to fail")
	}
}
