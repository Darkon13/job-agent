package core

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestProfileStateProposalUsesDeclaredFieldsAndRedactedDiff(t *testing.T) {
	now := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	resource, err := NewProfileStateResource("primary-backend", "primary", ProfileStateOwnershipDeclaredFields, json.RawMessage(`{
		"resumes":{"backend":{"about":"Новый текст","salary":{"amount":250000},"skills":["Go","PostgreSQL"]}},
		"profile":{"first_name":"Иван","middle_name":null}
	}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	observation, err := NewProfileStateObservation("primary", json.RawMessage(`{
		"profile":{"first_name":"Иван","middle_name":"Иванович","last_name":"Скрытое поле"},
		"resumes":{"backend":{"about":"Старый текст","salary":{"amount":200000,"currency":"RUR"},"skills":["Go"]}}
	}`), "revision-42", now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("new observation: %v", err)
	}
	proposal, err := NewProfileStateProposal("proposal-1", resource, observation, now)
	if err != nil {
		t.Fatalf("new proposal: %v", err)
	}
	wantPaths := []string{"/profile/middle_name", "/resumes/backend/about", "/resumes/backend/salary/amount", "/resumes/backend/skills"}
	if proposal.Status != ProfileStateProposalPlanned || len(proposal.Changes) != len(wantPaths) {
		t.Fatalf("proposal status/changes: %#v", proposal)
	}
	for index, path := range wantPaths {
		if proposal.Changes[index].Path != path || proposal.Changes[index].BeforeDigest == "" || proposal.Changes[index].AfterDigest == "" {
			t.Fatalf("change %d = %#v, want path %s with digests", index, proposal.Changes[index], path)
		}
	}
	if proposal.Changes[0].Operation != "clear" {
		t.Fatalf("middle name change = %#v, want clear", proposal.Changes[0])
	}
	encoded, err := json.Marshal(proposal)
	if err != nil {
		t.Fatalf("marshal proposal: %v", err)
	}
	for _, secret := range []string{"Новый текст", "Старый текст", "Иванович", "250000"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("public proposal JSON leaked value %q: %s", secret, encoded)
		}
	}
}

func TestProfileStateProposalIsStableAndDetectsNoChanges(t *testing.T) {
	now := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	first, err := NewProfileStateResource("resource", "primary", ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"profile":{"first_name":"Иван"}}`))
	if err != nil {
		t.Fatalf("new first resource: %v", err)
	}
	second, err := NewProfileStateResource("resource", "primary", ProfileStateOwnershipDeclaredFields, json.RawMessage(`{ "profile": { "first_name": "Иван" } }`))
	if err != nil {
		t.Fatalf("new second resource: %v", err)
	}
	if string(first.State) != string(second.State) || first.ManifestDigest != second.ManifestDigest {
		t.Fatalf("resource canonicalization differs: %#v %#v", first, second)
	}
	paths, err := first.DeclaredPaths()
	if err != nil {
		t.Fatalf("declared paths: %v", err)
	}
	if got, want := strings.Join(paths, ","), "/profile/first_name"; got != want {
		t.Fatalf("declared paths: got %q want %q", got, want)
	}
	observation, err := NewProfileStateObservation("primary", first.State, "", now)
	if err != nil {
		t.Fatalf("new observation: %v", err)
	}
	proposal, err := NewProfileStateProposal("proposal", first, observation, now)
	if err != nil {
		t.Fatalf("new proposal: %v", err)
	}
	if proposal.Status != ProfileStateProposalNoChanges || len(proposal.Changes) != 0 {
		t.Fatalf("proposal = %#v, want no changes", proposal)
	}
}

func TestProfileStateResourceRejectsInvalidShapeAndEmptyOwnership(t *testing.T) {
	for _, state := range []string{
		`{}`,
		`{"unknown":{"value":1}}`,
		`{"profile":[]}`,
		`{"resumes":{"backend":"text"}}`,
		`{"profile":{"nested":{}}}`,
		`{"resumes":{"backend":{"about":{"processor":"about-backend"}}}}`,
		`{"resumes":{"backend":{"skills":[{"processor":"skills"}]}}}`,
	} {
		if _, err := NewProfileStateResource("resource", "primary", ProfileStateOwnershipDeclaredFields, json.RawMessage(state)); err == nil {
			t.Fatalf("expected state to fail: %s", state)
		}
	}
}

func TestProfileStateResourceDerivesOneShotLeafOverrides(t *testing.T) {
	resource, err := NewProfileStateResource("resource", "primary", ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"backend":{"about":"from config","skills":["Go"]}}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	derived, err := resource.WithOverrides([]ProfileStateValueOverride{{
		Path: "/resumes/backend/about", Value: json.RawMessage(`"from dashboard"`),
	}})
	if err != nil {
		t.Fatalf("derive resource: %v", err)
	}
	if string(resource.State) != `{"resumes":{"backend":{"about":"from config","skills":["Go"]}}}` {
		t.Fatalf("source resource was mutated: %s", resource.State)
	}
	value, exists, err := derived.ValueAt("/resumes/backend/about")
	if err != nil || !exists || string(value) != `"from dashboard"` {
		t.Fatalf("derived value = %s exists=%v err=%v", value, exists, err)
	}
	if derived.ManifestDigest == resource.ManifestDigest {
		t.Fatal("one-shot override must have its own manifest digest")
	}
	paths, err := derived.DeclaredPaths()
	if err != nil || strings.Join(paths, ",") != "/resumes/backend/about,/resumes/backend/skills" {
		t.Fatalf("derived paths = %v err=%v", paths, err)
	}
}

func TestProfileStateResourceRejectsOwnershipChangingOverrides(t *testing.T) {
	resource, err := NewProfileStateResource("resource", "primary", ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"backend":{"about":"from config"}}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	for _, overrides := range [][]ProfileStateValueOverride{
		{{Path: "/resumes/backend/missing", Value: json.RawMessage(`"value"`)}},
		{{Path: "/resumes/backend/about", Value: json.RawMessage(`{"nested":"value"}`)}},
		{{Path: "/resumes/backend/about", Value: json.RawMessage(`"one"`)}, {Path: "/resumes/backend/about", Value: json.RawMessage(`"two"`)}},
		{{Path: "/resumes/backend/about", Value: json.RawMessage(`not-json`)}},
	} {
		if _, err := resource.WithOverrides(overrides); err == nil {
			t.Fatalf("expected overrides to fail: %#v", overrides)
		}
	}
}

func TestProfileStateObservationChecksBootstrapPathsConservatively(t *testing.T) {
	now := time.Now().UTC()
	empty, err := NewProfileStateObservation("primary", json.RawMessage(`{
		"profile":{"name":"","middle_name":null,"areas":[],"extra":{}},
		"resumes":{}
	}`), "", now)
	if err != nil {
		t.Fatalf("new empty observation: %v", err)
	}
	paths := []string{"/profile/name", "/profile/middle_name", "/profile/areas", "/profile/extra", "/resumes/missing/about"}
	if result, err := empty.PathsEmpty(paths); err != nil || !result {
		t.Fatalf("empty paths = %t, err=%v", result, err)
	}

	for name, state := range map[string]string{
		"text":    `{"profile":{"value":"set"}}`,
		"number":  `{"profile":{"value":0}}`,
		"boolean": `{"profile":{"value":false}}`,
		"array":   `{"profile":{"value":["Go"]}}`,
		"object":  `{"profile":{"value":{"id":"1"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			observation, err := NewProfileStateObservation("primary", json.RawMessage(state), "", now)
			if err != nil {
				t.Fatalf("new observation: %v", err)
			}
			if result, err := observation.PathsEmpty([]string{"/profile/value"}); err != nil || result {
				t.Fatalf("populated path = %t, err=%v", result, err)
			}
		})
	}

	if _, err := empty.PathsEmpty(nil); err == nil {
		t.Fatal("expected empty path list to fail")
	}
	if _, err := empty.PathsEmpty([]string{"profile/name"}); err == nil {
		t.Fatal("expected invalid JSON Pointer to fail")
	}
}

func TestProfileStateObservationValueAtDistinguishesMissingAndNull(t *testing.T) {
	observation, err := NewProfileStateObservation("primary", json.RawMessage(`{
		"profile":{"middle_name":null},
		"resumes":{"backend":{"skills":["Go"]}}
	}`), "", time.Now().UTC())
	if err != nil {
		t.Fatalf("new observation: %v", err)
	}
	value, exists, err := observation.ValueAt("/resumes/backend/skills")
	if err != nil || !exists || string(value) != `["Go"]` {
		t.Fatalf("skills=%s exists=%v err=%v", value, exists, err)
	}
	value, exists, err = observation.ValueAt("/profile/middle_name")
	if err != nil || !exists || string(value) != "null" {
		t.Fatalf("middle name=%s exists=%v err=%v", value, exists, err)
	}
	if value, exists, err = observation.ValueAt("/profile/missing"); err != nil || exists || value != nil {
		t.Fatalf("missing=%s exists=%v err=%v", value, exists, err)
	}
	if _, _, err := observation.ValueAt("profile/middle_name"); err == nil {
		t.Fatal("expected invalid JSON pointer to fail")
	}
}

func TestProfileStateChangeEscapesJSONPointer(t *testing.T) {
	now := time.Now().UTC()
	resource, err := NewProfileStateResource("resource", "primary", ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"profile":{"a/b~c":true}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	observation, err := NewProfileStateObservation("primary", json.RawMessage(`{"profile":{}}`), "", now)
	if err != nil {
		t.Fatalf("new observation: %v", err)
	}
	proposal, err := NewProfileStateProposal("proposal", resource, observation, now)
	if err != nil {
		t.Fatalf("new proposal: %v", err)
	}
	if len(proposal.Changes) != 1 || proposal.Changes[0].Path != "/profile/a~1b~0c" || proposal.Changes[0].BeforePresent {
		t.Fatalf("changes = %#v", proposal.Changes)
	}
}

func TestProfileStateProposalClassifiesPartialApplyAndConflict(t *testing.T) {
	now := time.Now().UTC()
	resource, err := NewProfileStateResource("resource", "primary", ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"one":{"about":"new-1"},"two":{"about":"new-2"}},"profile":{"area":"1"}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	before, err := NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"one":{"about":"old-1"},"two":{"about":"old-2"}},"profile":{"area":"1"}}`), "", now)
	if err != nil {
		t.Fatalf("new before observation: %v", err)
	}
	proposal, err := NewProfileStateProposal("proposal", resource, before, now)
	if err != nil {
		t.Fatalf("new proposal: %v", err)
	}
	partial, err := NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"one":{"about":"new-1"},"two":{"about":"old-2"}},"profile":{"area":"1"}}`), "", now)
	if err != nil {
		t.Fatalf("new partial observation: %v", err)
	}
	pending, err := proposal.ChangesToApply(partial)
	if err != nil || len(pending) != 1 || pending[0].Path != "/resumes/two/about" {
		t.Fatalf("pending = %#v err=%v", pending, err)
	}
	conflict, err := NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"one":{"about":"third"},"two":{"about":"old-2"}},"profile":{"area":"1"}}`), "", now)
	if err != nil {
		t.Fatalf("new conflict observation: %v", err)
	}
	if _, err := proposal.ChangesToApply(conflict); !errors.Is(err, ErrProfileStateChanged) {
		t.Fatalf("conflict error = %v", err)
	}
	unchangedFieldConflict, err := NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"one":{"about":"old-1"},"two":{"about":"old-2"}},"profile":{"area":"2"}}`), "", now)
	if err != nil {
		t.Fatalf("new unchanged-field conflict observation: %v", err)
	}
	if _, err := proposal.ChangesToApply(unchangedFieldConflict); !errors.Is(err, ErrProfileStateChanged) {
		t.Fatalf("unchanged field conflict error = %v", err)
	}
}

func TestProfileStateRevisionRequiresAppliedProposal(t *testing.T) {
	now := time.Now().UTC()
	resource, err := NewProfileStateResource("resource", "primary", ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"one":{"about":"new"}}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	before, err := NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"one":{"about":"old"}}}`), "", now)
	if err != nil {
		t.Fatalf("new observation: %v", err)
	}
	proposal, err := NewProfileStateProposal("proposal", resource, before, now)
	if err != nil {
		t.Fatalf("new proposal: %v", err)
	}
	revision, err := NewProfileStateRevision(proposal, "cron:refresh-about", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("new revision: %v", err)
	}
	if revision.ProposalID != proposal.ID || revision.Source != "cron:refresh-about" ||
		len(revision.Changes) != 1 || revision.Changes[0].Path != "/resumes/one/about" {
		t.Fatalf("revision = %#v", revision)
	}
	if _, err := NewProfileStateRevision(proposal, "  ", now); err == nil {
		t.Fatal("expected empty source to fail")
	}
	if _, err := NewProfileStateRevision(proposal, "cron", time.Time{}); err == nil {
		t.Fatal("expected zero applied time to fail")
	}
	same, err := NewProfileStateObservation("primary", resource.State, "", now)
	if err != nil {
		t.Fatalf("new same observation: %v", err)
	}
	noChanges, err := NewProfileStateProposal("proposal-2", resource, same, now)
	if err != nil {
		t.Fatalf("new no-changes proposal: %v", err)
	}
	if _, err := NewProfileStateRevision(noChanges, "cron", now); err == nil {
		t.Fatal("expected a no-changes proposal to be rejected")
	}
}
