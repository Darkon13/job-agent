package hh

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/Darkon13/job-agent/core"
	"testing"
)

func TestValidateSearchSource(t *testing.T) {
	instance, err := New(nil)
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	tests := []struct {
		name    string
		query   string
		wantErr bool
	}{
		{name: "global", query: `{"source":"global"}`},
		{name: "similar resume", query: `{"source":"similar_resume","resume":"backend"}`},
		{name: "similar vacancy", query: `{"source":"similar_vacancy","vacancy":"123"}`},
		{name: "related vacancy", query: `{"source":"related_vacancy","vacancy":"123"}`},
		{name: "similar resume with modern work field", query: `{"source":"similar_resume","resume":"backend","work_format":["REMOTE"]}`},
		{name: "similar vacancy with modern work field", query: `{"source":"similar_vacancy","vacancy":"123","employment_form":["FULL"]}`},
		{name: "related vacancy with filter", query: `{"source":"related_vacancy","vacancy":"123","text":"Go"}`, wantErr: true},
		{name: "related vacancy with pagination", query: `{"source":"related_vacancy","vacancy":"123","page_size":10,"max_pages":2}`},
		{name: "missing source", query: `{}`, wantErr: true},
		{name: "similar without resume", query: `{"source":"similar_resume"}`, wantErr: true},
		{name: "vacancy source without vacancy", query: `{"source":"similar_vacancy"}`, wantErr: true},
		{name: "global with resume", query: `{"source":"global","resume":"backend"}`, wantErr: true},
		{name: "partial geo", query: `{"source":"global","top_lat":1}`, wantErr: true},
		{name: "period with date", query: `{"source":"global","period":7,"date_from":"2026-01-01"}`, wantErr: true},
		{name: "period over api maximum", query: `{"source":"global","period":31}`, wantErr: true},
		{name: "bounded browser page", query: `{"source":"global","page_size":5,"max_pages":1}`},
		{name: "page size over maximum", query: `{"source":"global","page_size":101}`, wantErr: true},
		{name: "pages over maximum", query: `{"source":"global","max_pages":21}`, wantErr: true},
		{name: "empty repeated filter", query: `{"source":"global","area":["1",""]}`, wantErr: true},
		{name: "duplicate repeated filter", query: `{"source":"global","area":["1","1"]}`, wantErr: true},
		{name: "invalid date", query: `{"source":"global","date_from":"tomorrow"}`, wantErr: true},
		{name: "unknown field", query: `{"source":"global","typo_filter":"ignored-by-hh"}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := instance.ValidateSearch(json.RawMessage(tt.query))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateSearch() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSupportsSearchForBrowserOnlyProfile(t *testing.T) {
	instance, err := New(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	concrete, ok := instance.(*Adapter)
	if !ok {
		t.Fatal("adapter is not concrete")
	}
	stateFile := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(stateFile, []byte(`{"cookies":[],"origins":[]}`), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if _, err := concrete.BindBrowserSession("secondary", stateFile); err != nil {
		t.Fatalf("bind browser session: %v", err)
	}
	checker, ok := instance.(interface {
		SupportsSearch(core.ProfileID, json.RawMessage) error
	})
	if !ok {
		t.Fatal("adapter does not check search support")
	}
	cases := []struct {
		name    string
		query   string
		wantErr bool
	}{
		{name: "global", query: `{"source":"global"}`},
		{name: "similar resume", query: `{"source":"similar_resume","resume":"r1"}`},
		{name: "similar vacancy", query: `{"source":"similar_vacancy","vacancy":"42"}`, wantErr: true},
		{name: "related vacancy", query: `{"source":"related_vacancy","vacancy":"42"}`, wantErr: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := checker.SupportsSearch("secondary", json.RawMessage(test.query))
			if (err != nil) != test.wantErr {
				t.Fatalf("SupportsSearch() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}
