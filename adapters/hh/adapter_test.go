package hh

import (
	"encoding/json"
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
		{name: "missing source", query: `{}`, wantErr: true},
		{name: "similar without resume", query: `{"source":"similar_resume"}`, wantErr: true},
		{name: "vacancy source without vacancy", query: `{"source":"similar_vacancy"}`, wantErr: true},
		{name: "global with resume", query: `{"source":"global","resume":"backend"}`, wantErr: true},
		{name: "partial geo", query: `{"source":"global","top_lat":1}`, wantErr: true},
		{name: "period with date", query: `{"source":"global","period":7,"date_from":"2026-01-01"}`, wantErr: true},
		{name: "period over api maximum", query: `{"source":"global","period":31}`, wantErr: true},
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
