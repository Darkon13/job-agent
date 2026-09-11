package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Darkon13/job-agent/core"
)

func TestResumeAPIExposesConfiguredTargets(t *testing.T) {
	api, err := NewResumeAPI(map[core.ProfileID][]core.ResumeTarget{
		"primary": {
			{ID: "resume-1", Primary: true},
			{ID: "resume-2", Alias: "front"},
		},
	})
	if err != nil {
		t.Fatalf("api: %v", err)
	}
	handler := api.Handler(nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/profiles/primary/resumes", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var body listResponse[core.ResumeTarget]
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 2 || !body.Items[0].Primary || body.Items[1].Alias != "front" {
		t.Fatalf("items = %#v", body.Items)
	}
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/v1/profiles/other/resumes", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown profile status = %d", missing.Code)
	}
}
