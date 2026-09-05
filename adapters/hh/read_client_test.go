package hh

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

const testAccessToken = "secret-access-token"

func newReadClientFixture(t *testing.T, handler http.Handler) *ReadClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "credentials.json")
	data, err := json.Marshal(oauthCredentials{AccessToken: testAccessToken})
	if err != nil {
		t.Fatalf("encode credentials: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	client, err := NewReadClient("primary", "file:"+path, "JobAgent/Test (test@example.com)", server.Client())
	if err != nil {
		t.Fatalf("new read client: %v", err)
	}
	client.apiBaseURL = server.URL
	return client
}

func TestReadClientReadsApplicantProfile(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/me" {
			t.Errorf("path = %q, want /me", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+testAccessToken {
			t.Error("missing bearer authorization")
		}
		if request.Header.Get("HH-User-Agent") != "JobAgent/Test (test@example.com)" {
			t.Errorf("unexpected HH-User-Agent %q", request.Header.Get("HH-User-Agent"))
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":"applicant-42","auth_type":"applicant","is_applicant":true}`))
	}))

	profile, err := client.ReadProfile(context.Background(), "primary")
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	if profile.ExternalAccountID != "applicant-42" || profile.AuthType != "applicant" {
		t.Fatalf("unexpected profile: %#v", profile)
	}
}

func TestReadClientNormalizesUnauthorizedWithoutLeakingToken(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusForbidden)
		_, _ = response.Write([]byte(`{"description":"` + testAccessToken + `"}`))
	}))

	_, err := client.ReadProfile(context.Background(), "primary")
	if !core.ErrorIsCategory(err, core.ErrorUnauthorized) {
		t.Fatalf("error = %v, want unauthorized", err)
	}
	if strings.Contains(err.Error(), testAccessToken) {
		t.Fatal("operation error leaked access token")
	}
}

func TestReadClientNormalizesTemporaryFailure(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
	}))

	_, err := client.ReadProfile(context.Background(), "primary")
	if !core.ErrorIsCategory(err, core.ErrorTemporaryFailure) {
		t.Fatalf("error = %v, want temporary failure", err)
	}
}

func TestReadClientNormalizesPlatformRejection(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusBadRequest)
	}))

	_, err := client.ReadProfile(context.Background(), "primary")
	if !core.ErrorIsCategory(err, core.ErrorPermanentFailure) {
		t.Fatalf("error = %v, want permanent failure", err)
	}
}

func TestReadClientPreservesContextCancellation(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.ReadProfile(ctx, "primary")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
}

func TestReadClientSerializesChecksForOneProfile(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		_, _ = response.Write([]byte(`{"id":"42","auth_type":"applicant"}`))
	}))

	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := client.ReadProfile(context.Background(), "primary")
			results <- err
		}()
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first profile check did not start")
	}
	select {
	case <-entered:
		t.Fatal("second profile check bypassed per-profile lock")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("read profile: %v", err)
		}
	}
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent checks = %d, want 1", maximum.Load())
	}
}

func TestReadClientRejectsCredentialFileVisibleToOtherUsers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(`{"access_token":"token"}`), 0o644); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	client, err := NewReadClient("primary", path, "", &http.Client{})
	if err != nil {
		t.Fatalf("new read client: %v", err)
	}
	_, err = client.ReadProfile(context.Background(), "primary")
	if !core.ErrorIsCategory(err, core.ErrorUnauthorized) {
		t.Fatalf("error = %v, want unauthorized", err)
	}
}
