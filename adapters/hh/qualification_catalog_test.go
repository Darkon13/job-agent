package hh

import (
	"context"
	stdhtml "html"
	"net/http"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

const qualificationCatalogFixture = `<html><body>
	<div data-qa="skills-verification-method-container">
		<a href="/applicant/skills/123/verification_methods">Go</a>
		<div data-qa="applicant-keyskills-verification-methods-level-tab">Лёгкий</div>
		<div data-qa="applicant-keyskills-verification-methods-level-tab">Средний</div>
		<div data-qa="applicant-keyskills-verification-methods-level-tab">Сложный</div>
		<div data-qa="applicant-keyskills-verification-methods-kind-card-theory">
			<button data-qa="applicant-keyskills-verification-methods-start-theory">Начать</button>
		</div>
	</div>
	<div data-qa="skills-verification-method-container">
		<div data-qa="verification-method-title">SQL</div>
		<a href="/applicant/skills/456/verification_methods">SQL</a>
	</div>
</body></html>`

func TestParseQualificationCatalogNormalizesLevels(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	offerings, err := ParseQualificationCatalog([]byte(qualificationCatalogFixture), Name, "primary", now)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(offerings) != 4 {
		t.Fatalf("offerings = %#v", offerings)
	}
	byExternal := make(map[string]core.QualificationOffering, len(offerings))
	for _, offering := range offerings {
		byExternal[offering.ExternalID] = offering
	}
	sql, exists := byExternal["456:default:theory"]
	if !exists || sql.Qualification.FamilyName != "SQL" {
		t.Fatalf("sql offering = %#v", byExternal)
	}
	easy, exists := byExternal["123:easy:theory"]
	if !exists || easy.Qualification.LevelName != "Лёгкий" || easy.Qualification.FamilyName != "Go" {
		t.Fatalf("easy offering = %#v", easy)
	}
	if easy.Qualification.LevelOrder == nil || *easy.Qualification.LevelOrder != 0 {
		t.Fatalf("level order = %#v", easy.Qualification.LevelOrder)
	}
	if easy.Status != core.QualificationAvailable || easy.ProfileID != "primary" || !easy.ObservedAt.Equal(now) {
		t.Fatalf("offering = %#v", easy)
	}
	if _, exists := byExternal["123:hard:theory"]; !exists {
		t.Fatalf("missing hard offering: %#v", byExternal)
	}
}

func TestParseQualificationCatalogRejectsUnknownPage(t *testing.T) {
	_, err := ParseQualificationCatalog([]byte(`<html><body>no catalog</body></html>`), Name, "primary", time.Now().UTC())
	requireOperationCategory(t, err, core.ErrorUnsupported)
}

func TestBrowserSyncQualificationsReadsMethodsPage(t *testing.T) {
	client := newBrowserReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/applicant/skill_verifications/methods" {
			t.Errorf("path = %q", request.URL.Path)
		}
		_, _ = response.Write([]byte(qualificationCatalogFixture))
	}))
	offerings, err := client.SyncQualifications(context.Background(), "primary")
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(offerings) != 4 {
		t.Fatalf("offerings = %d", len(offerings))
	}
}

func TestBrowserSyncQualificationsRequiresLogin(t *testing.T) {
	client := newBrowserReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/applicant/skill_verifications/methods" {
			http.Redirect(response, request, "/account/login", http.StatusFound)
			return
		}
		_, _ = response.Write([]byte(`<html><body>login</body></html>`))
	}))
	_, err := client.SyncQualifications(context.Background(), "primary")
	requireOperationCategory(t, err, core.ErrorUnauthorized)
}

func qualificationInitialStateFixture(t *testing.T) string {
	t.Helper()
	state := `{"skillsVerificationMethodsPage":{"items":[
		{"id":322406,"name":"Golang","category":"SKILL","levels":[
			{"id":8,"internalId":"base","name":"Базовый","rank":1,
			 "theory":{"externalId":"dee21739","availability":{"status":"AVAILABLE"}},
			 "practice":{"externalId":"7fc6eb8b","availability":{"status":"AVAILABLE"}}},
			{"id":9,"internalId":"middle","name":"Средний","rank":2,
			 "theory":{"externalId":"8a02d3e2","availability":{"status":"AVAILABLE"}},
			 "practice":{"externalId":"f6ccad0f","availability":{"status":"UNAVAILABLE"}}}
		]},
		{"id":57,"name":"Английский","category":"LANG","levels":[
			{"id":1,"internalId":"a1","name":"A1 — Начальный","rank":1,
			 "theory":{"externalId":"58bcf028","availability":{"status":"AVAILABLE"}},"practice":null}
		]}
	]}}`
	return `<html><body><template id="HH-Lux-InitialState">` + stdhtml.EscapeString(state) + `</template></body></html>`
}

func TestParseQualificationCatalogReadsInitialState(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	offerings, err := ParseQualificationCatalog([]byte(qualificationInitialStateFixture(t)), Name, "primary", now)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(offerings) != 4 {
		t.Fatalf("offerings = %#v", offerings)
	}
	byExternal := make(map[string]core.QualificationOffering, len(offerings))
	for _, offering := range offerings {
		byExternal[offering.ExternalID] = offering
	}
	base, exists := byExternal["322406:base:theory"]
	if !exists || base.Qualification.FamilyName != "Golang" || base.Qualification.LevelName != "Базовый" ||
		base.Qualification.LevelOrder == nil || *base.Qualification.LevelOrder != 0 {
		t.Fatalf("base offering = %#v", base)
	}
	if _, exists := byExternal["322406:base:practice"]; !exists {
		t.Fatalf("missing base practice: %#v", byExternal)
	}
	middle, exists := byExternal["322406:middle:theory"]
	if !exists || middle.Qualification.LevelOrder == nil || *middle.Qualification.LevelOrder != 1 {
		t.Fatalf("middle offering = %#v", middle)
	}
	if _, exists := byExternal["322406:middle:practice"]; exists {
		t.Fatalf("unavailable practice was included: %#v", byExternal)
	}
	if _, exists := byExternal["57:a1:theory"]; !exists {
		t.Fatalf("missing language offering: %#v", byExternal)
	}
}

func TestParseQualificationCatalogRejectsEmptyInitialState(t *testing.T) {
	page := `<html><body><template id="HH-Lux-InitialState">{"skillsVerificationMethodsPage":{"items":[]}}</template></body></html>`
	_, err := ParseQualificationCatalog([]byte(page), Name, "primary", time.Now().UTC())
	requireOperationCategory(t, err, core.ErrorUnsupported)
}
