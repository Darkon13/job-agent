package hh

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/Darkon13/job-agent/core"
)

func TestApplicationStateObserverReadsEveryPageAndNormalizesDisposition(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/negotiations" || request.URL.Query().Get("status") != "all" || request.URL.Query().Get("per_page") != strconv.Itoa(applicationStatePageSize) {
			t.Errorf("unexpected request: %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
		}
		page := request.URL.Query().Get("page")
		response.Header().Set("Content-Type", "application/json")
		switch page {
		case "0":
			_, _ = fmt.Fprint(response, `{"pages":2,"page":0,"items":[{"id":"n-1","state":{"id":"response"},"updated_at":"2026-09-01T12:00:00+0300","viewed_by_opponent":true,"vacancy":{"id":"v-1"}},{"id":"n-2","state":{"id":"invitation"},"vacancy":{"id":"v-2"}}]}`)
		case "1":
			_, _ = fmt.Fprint(response, `{"pages":2,"page":1,"items":[{"id":"n-3","state":{"id":"discard"},"viewed_by_opponent":false,"vacancy":{"id":"v-3"}},{"id":"n-4","state":{"id":"future_state"},"vacancy":{"id":"v-4"}}]}`)
		default:
			t.Fatalf("unexpected page %q", page)
		}
	}))

	result, err := client.ObserveApplicationStates(context.Background(), "primary")
	if err != nil {
		t.Fatalf("observe states: %v", err)
	}
	if len(result.Applications) != 4 || result.ObservedAt.IsZero() {
		t.Fatalf("observation: %#v", result)
	}
	want := []core.ApplicationDisposition{
		core.ApplicationDispositionPending, core.ApplicationDispositionInvited,
		core.ApplicationDispositionRejected, core.ApplicationDispositionUnknown,
	}
	for index, disposition := range want {
		if result.Applications[index].Disposition != disposition {
			t.Fatalf("item %d disposition=%s want=%s", index, result.Applications[index].Disposition, disposition)
		}
	}
	if result.Applications[0].PlatformUpdatedAt == nil || result.Applications[0].ViewedByOpponent == nil || !*result.Applications[0].ViewedByOpponent {
		t.Fatalf("first observation metadata: %#v", result.Applications[0])
	}
}

func TestApplicationStateObserverClassifiesAuthorizationFailure(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusForbidden)
		_, _ = response.Write([]byte(`{"errors":[{"type":"oauth","value":"token_expired"}]}`))
	}))
	_, err := client.ObserveApplicationStates(context.Background(), "primary")
	if !core.ErrorIsCategory(err, core.ErrorUnauthorized) {
		t.Fatalf("error=%v, want unauthorized", err)
	}
}

func TestApplicationStateObserverRejectsIncompletePagination(t *testing.T) {
	for _, payload := range []string{
		`{"items":[]}`, `{"page":1,"pages":1,"items":[]}`, `{"page":0,"pages":2,"items":[]}`,
	} {
		t.Run(payload, func(t *testing.T) {
			client := newReadClientFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, payload) }))
			if result, err := client.ObserveApplicationStates(context.Background(), "primary"); err == nil || len(result.Applications) != 0 {
				t.Fatalf("partial observation escaped: %v %v", result, err)
			}
		})
	}
}

func TestApplicationStateObserverDeduplicatesPaginationOverlap(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Query().Get("page") {
		case "0":
			_, _ = fmt.Fprint(response, `{"pages":2,"page":0,"items":[{"id":"n-1","state":{"id":"response"},"updated_at":"2026-09-01T12:00:00+0300","vacancy":{"id":"v-1"}},{"id":"n-2","state":{"id":"invitation"},"vacancy":{"id":"v-2"}}]}`)
		case "1":
			_, _ = fmt.Fprint(response, `{"pages":2,"page":1,"items":[{"id":"n-2","state":{"id":"invitation"},"vacancy":{"id":"v-2"}},{"id":"n-3","state":{"id":"discard"},"vacancy":{"id":"v-3"}}]}`)
		default:
			t.Fatalf("unexpected page %q", request.URL.Query().Get("page"))
		}
	}))

	result, err := client.ObserveApplicationStates(context.Background(), "primary")
	if err != nil {
		t.Fatalf("observe states: %v", err)
	}
	if len(result.Applications) != 3 {
		t.Fatalf("observation = %#v", result.Applications)
	}
	seen := make(map[string]struct{}, len(result.Applications))
	for _, application := range result.Applications {
		if _, duplicate := seen[application.ExternalNegotiationID]; duplicate {
			t.Fatalf("duplicate negotiation %s in %#v", application.ExternalNegotiationID, result.Applications)
		}
		seen[application.ExternalNegotiationID] = struct{}{}
	}
}
