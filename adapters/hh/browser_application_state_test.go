package hh

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func negotiationsPage(payload string) string {
	return `<html><body><template id="HH-Lux-InitialState">` + payload + `</template></body></html>`
}

func TestBrowserObservationReadsNegotiationStatesAcrossPages(t *testing.T) {
	client := newBrowserReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/applicant/negotiations" || request.URL.Query().Get("status") != "all" {
			t.Errorf("unexpected negotiations URL: %s", request.URL.String())
		}
		switch request.URL.Query().Get("page") {
		case "0":
			_, _ = response.Write([]byte(negotiationsPage(`{
				"applicantNegotiations": {
					"topicList": [
						{"id": 111, "vacancyId": "42", "lastState": "RESPONSE", "viewedByOpponent": true, "lastModified": "2026-09-13T10:00:00+03:00"},
						{"id": 112, "vacancyId": 43, "lastState": "INTERVIEW", "viewedByOpponent": false, "lastModified": "2026-09-13T11:00:00+03:00"},
						{"id": 113, "vacancyId": "44", "lastState": "WEIRD_FUTURE_STATE", "lastModified": "2026-09-13T12:00:00+03:00"}
					],
					"pageCount": "2"
				}
			}`)))
		case "1":
			_, _ = response.Write([]byte(negotiationsPage(`{
				"applicantNegotiations": {
					"topicList": [
						{"id": 114, "vacancyId": "45", "lastState": "DISCARD", "viewedByOpponent": true, "lastModified": "2026-09-13T13:00:00+03:00"}
					],
					"pageCount": "2"
				}
			}`)))
		default:
			t.Errorf("unexpected negotiations page: %s", request.URL.Query().Get("page"))
		}
	}))

	result, err := client.ObserveApplicationStates(context.Background(), "primary")
	if err != nil {
		t.Fatalf("observe application states: %v", err)
	}
	if len(result.Applications) != 4 || result.ObservedAt.IsZero() {
		t.Fatalf("result = %#v", result)
	}
	byNegotiation := make(map[string]int, len(result.Applications))
	for index, application := range result.Applications {
		byNegotiation[application.ExternalNegotiationID] = index
	}
	expectations := map[string]struct {
		vacancyID   string
		disposition core.ApplicationDisposition
		viewed      bool
		updatedHour int
	}{
		"111": {vacancyID: "42", disposition: core.ApplicationDispositionPending, viewed: true, updatedHour: 7},
		"112": {vacancyID: "43", disposition: core.ApplicationDispositionInvited, updatedHour: 8},
		"113": {vacancyID: "44", disposition: core.ApplicationDispositionUnknown, updatedHour: 9},
		"114": {vacancyID: "45", disposition: core.ApplicationDispositionRejected, viewed: true, updatedHour: 10},
	}
	for negotiationID, expected := range expectations {
		index, exists := byNegotiation[negotiationID]
		if !exists {
			t.Fatalf("negotiation %s missing from %#v", negotiationID, result.Applications)
		}
		application := result.Applications[index]
		if application.ExternalVacancyID != expected.vacancyID || application.Disposition != expected.disposition {
			t.Fatalf("negotiation %s = %#v, want vacancy %s disposition %s", negotiationID, application, expected.vacancyID, expected.disposition)
		}
		if expected.viewed && (application.ViewedByOpponent == nil || !*application.ViewedByOpponent) {
			t.Fatalf("negotiation %s lost viewed flag: %#v", negotiationID, application)
		}
		if application.PlatformUpdatedAt == nil || application.PlatformUpdatedAt.In(time.UTC).Hour() != expected.updatedHour {
			t.Fatalf("negotiation %s update time = %#v", negotiationID, application.PlatformUpdatedAt)
		}
	}
}

func TestBrowserObservationFailsWithoutInitialState(t *testing.T) {
	client := newBrowserReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`<html><body>no state here</body></html>`))
	}))
	if _, err := client.ObserveApplicationStates(context.Background(), "primary"); err == nil {
		t.Fatal("expected observation to fail without the initial state")
	}
}

func TestBrowserVacancyReadReportsClosedVacancy(t *testing.T) {
	client := newBrowserReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`<html><body><h1>Вакансия закрыта</h1><p>Эта вакансия больше не доступна.</p></body></html>`))
	}))
	_, err := client.ReadVacancy(context.Background(), "primary", core.VacancyKey{Platform: Name, ExternalID: "42"})
	if err == nil {
		t.Fatal("expected a closed vacancy to be reported")
	}
	var operationError *core.OperationError
	if !errors.As(err, &operationError) || operationError.Category != core.ErrorValidationRequired || operationError.Metadata["code"] != "vacancy_closed" {
		t.Fatalf("unexpected error: %#v", err)
	}
}
