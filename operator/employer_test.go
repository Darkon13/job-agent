package operator

import (
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func TestEmployerGroupMatcherPrefersPlatformIDAndExplainsNestedMatch(t *testing.T) {
	matcher, err := NewEmployerGroupMatcher([]EmployerGroupConfig{
		{Tag: "ozon-companies", Rules: []EmployerGroupRuleConfig{
			{Platform: "hh", EmployerID: "2180"},
			{Name: "Ozon Tech"},
		}},
		{Tag: "marketplaces", Include: []string{"ozon-companies"}},
	})
	if err != nil {
		t.Fatalf("new matcher: %v", err)
	}
	vacancy := employerVacancy("hh", "Совсем другое имя", map[string]any{"employer_id": float64(2180)})
	matches := matcher.Match(vacancy)
	if len(matches) != 2 || matches[0].Evidence.Kind != "employer_id" || matches[0].Evidence.Value != "2180" ||
		matches[1].Evidence.Kind != "included_group" || matches[1].Evidence.Via != "ozon-companies" {
		t.Fatalf("matches = %#v", matches)
	}
}

func TestEmployerGroupMatcherUsesExactNormalizedNameWithoutSubstring(t *testing.T) {
	matcher, err := NewEmployerGroupMatcher([]EmployerGroupConfig{{
		Tag: "ozon", Rules: []EmployerGroupRuleConfig{{Name: "  OZON   Tech "}},
	}})
	if err != nil {
		t.Fatalf("new matcher: %v", err)
	}
	if matches := matcher.Match(employerVacancy("hh", "Ozon Tech", nil)); len(matches) != 1 || matches[0].Evidence.Kind != "name" {
		t.Fatalf("exact matches = %#v", matches)
	}
	if matches := matcher.Match(employerVacancy("hh", "Ozon Technologies", nil)); len(matches) != 0 {
		t.Fatalf("substring must not match: %#v", matches)
	}
}

func TestEmployerGroupMatcherRejectsInvalidGraphAndIdentities(t *testing.T) {
	tests := []struct {
		name   string
		groups []EmployerGroupConfig
		want   string
	}{
		{name: "unknown include", groups: []EmployerGroupConfig{{Tag: "a", Include: []string{"missing"}}}, want: "unknown group"},
		{name: "cycle", groups: []EmployerGroupConfig{{Tag: "a", Include: []string{"b"}}, {Tag: "b", Include: []string{"a"}}}, want: "a -> b -> a"},
		{name: "ambiguous", groups: []EmployerGroupConfig{{Tag: "a", Rules: []EmployerGroupRuleConfig{{Platform: "hh", EmployerID: "1", Name: "A"}}}}, want: "exactly one"},
		{name: "unscoped id", groups: []EmployerGroupConfig{{Tag: "a", Rules: []EmployerGroupRuleConfig{{EmployerID: "1"}}}}, want: "requires platform"},
		{name: "duplicate name", groups: []EmployerGroupConfig{{Tag: "a", Rules: []EmployerGroupRuleConfig{{Name: "Ozon"}, {Name: " ozon "}}}}, want: "duplicate identity"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewEmployerGroupMatcher(test.groups)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func employerVacancy(platform core.Platform, employer string, attributes map[string]any) core.Vacancy {
	return core.Vacancy{
		Platform: platform, ExternalID: "42", URL: "https://example.test/42", Title: "Backend",
		Employer: employer, State: core.VacancyStateOpen, ObservedAt: time.Now().UTC(), Attributes: attributes,
	}
}
