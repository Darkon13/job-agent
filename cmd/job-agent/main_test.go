package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Darkon13/job-agent/adapter"
	appconfig "github.com/Darkon13/job-agent/config"
	"github.com/Darkon13/job-agent/core"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
)

type profileReaderStub struct {
	result adapter.ProfileReadResult
	err    error
	calls  int
}

func (reader *profileReaderStub) ReadProfile(context.Context, core.ProfileID) (adapter.ProfileReadResult, error) {
	reader.calls++
	return reader.result, reader.err
}

type profileAdapterStub struct {
	reader adapter.ProfileReader
}

func TestParseMainOptions(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
		want      mainOptions
		wantErr   bool
	}{
		{name: "compatible positional config", arguments: []string{"config.json"}, want: mainOptions{configPath: "config.json"}},
		{name: "migrate before startup", arguments: []string{"-migrate-up", "/config/config.json"}, want: mainOptions{configPath: "/config/config.json", migrateUp: true}},
		{name: "missing config", arguments: []string{"-migrate-up"}, wantErr: true},
		{name: "extra positional argument", arguments: []string{"one.json", "two.json"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseMainOptions(test.arguments)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseMainOptions() error = %v, wantErr %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("parseMainOptions() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func (stub *profileAdapterStub) Name() string                         { return "stub" }
func (stub *profileAdapterStub) Capabilities() []core.Capability      { return nil }
func (stub *profileAdapterStub) ValidateSearch(json.RawMessage) error { return nil }
func (stub *profileAdapterStub) Search(context.Context, core.ProfileID, json.RawMessage, string) (core.SearchPage, error) {
	return core.SearchPage{}, errors.New("not implemented")
}
func (stub *profileAdapterStub) ReadVacancy(context.Context, core.ProfileID, core.VacancyKey) (core.Vacancy, error) {
	return core.Vacancy{}, errors.New("not implemented")
}
func (stub *profileAdapterStub) NewProfileReader(core.ProfileID, string) (adapter.ProfileReader, error) {
	return stub.reader, nil
}

func TestProbeProfileAuthorizationsMarksUnauthorizedProfile(t *testing.T) {
	reader := &profileReaderStub{err: &core.OperationError{
		Category: core.ErrorUnauthorized, Operation: "profiles.read", Platform: "stub",
	}}
	profiles := []appconfig.Profile{
		{Tag: "primary", Adapter: "platform", CredentialsRef: "file:/secret", Enabled: true},
		{Tag: "disabled", Adapter: "platform", CredentialsRef: "file:/secret", Enabled: false},
	}
	states, err := probeProfileAuthorizations(context.Background(), profiles, map[string]adapter.Adapter{
		"platform": &profileAdapterStub{reader: reader},
	})
	if err != nil {
		t.Fatalf("probe profiles: %v", err)
	}
	if states["primary"].Status != core.ProfileAuthRequired || states["disabled"].Status != core.ProfileDisabled {
		t.Fatalf("unexpected profile states: %#v", states)
	}
	if reader.calls != 1 {
		t.Fatalf("profile reads = %d, want 1", reader.calls)
	}
}

func TestProbeProfileAuthorizationsRetainsAuthenticatedReader(t *testing.T) {
	reader := &profileReaderStub{result: adapter.ProfileReadResult{ExternalAccountID: "account-42", AuthType: "applicant"}}
	profiles, err := probeProfileAuthorizations(context.Background(), []appconfig.Profile{{
		Tag: "primary", Adapter: "platform", CredentialsRef: "file:/secret", Enabled: true,
	}}, map[string]adapter.Adapter{"platform": &profileAdapterStub{reader: reader}})
	if err != nil {
		t.Fatalf("probe profiles: %v", err)
	}
	runtime := profiles["primary"]
	if runtime.Status != core.ProfileEnabled || runtime.ExternalAccountID != "account-42" || runtime.Reader != reader {
		t.Fatalf("unexpected runtime profile: %#v", runtime)
	}
}

func TestProbeProfileAuthorizationsReturnsTransportFailure(t *testing.T) {
	reader := &profileReaderStub{err: &core.OperationError{
		Category: core.ErrorTemporaryFailure, Operation: "profiles.read", Platform: "stub",
	}}
	_, err := probeProfileAuthorizations(context.Background(), []appconfig.Profile{{
		Tag: "primary", Adapter: "platform", CredentialsRef: "file:/secret", Enabled: true,
	}}, map[string]adapter.Adapter{"platform": &profileAdapterStub{reader: reader}})
	if !core.ErrorIsCategory(err, core.ErrorTemporaryFailure) {
		t.Fatalf("error = %v, want temporary failure", err)
	}
}

func TestApplicationPreparerRejectsInvalidTemplateAtComposition(t *testing.T) {
	_, err := applicationPreparer(appconfig.Profile{Applications: appconfig.ApplicationPolicy{
		MessageTemplate: "{{.Missing}}",
	}})
	if err == nil {
		t.Fatal("expected invalid application message template")
	}
}

func TestConfigureApplicationCampaignsBuildsScheduledRoute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job-agent.db")
	if err := storesqlite.MigrateUp(path); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	store, err := storesqlite.Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reader := &profileReaderStub{}
	instance := &profileAdapterStub{reader: reader}
	cfg := appconfig.Config{
		Searches: []appconfig.Search{{
			Tag: "golang", Adapter: "platform", Profiles: []string{"primary"}, Query: json.RawMessage(`{}`),
		}},
		Jobs: []appconfig.Job{{
			Tag: "daily", Enabled: true, Concurrency: appconfig.JobConcurrencyForbid,
			Triggers: []appconfig.JobTrigger{{Type: "cron", Expression: "30 9 * * *", Timezone: "Europe/Moscow", Misfire: "run_once"}},
			Action: appconfig.JobAction{
				Type: appconfig.JobActionApplicationCampaign, Profiles: []string{"primary"}, Routes: []string{"golang"},
				TargetSuccessful: 20, MaxInFlight: 3,
			},
		}},
	}
	handler, definitions, routes, jobs, err := configureApplicationCampaigns(
		cfg, map[string]adapter.Adapter{"platform": instance}, map[core.ProfileID]profileRuntime{
			"primary": {Status: core.ProfileEnabled, Reader: reader},
		}, store,
	)
	if err != nil {
		t.Fatalf("configure campaigns: %v", err)
	}
	if handler == nil || jobs != 1 || len(definitions) != 1 {
		t.Fatalf("handler=%v jobs=%d definitions=%d", handler != nil, jobs, len(definitions))
	}
	if definitions[0].ActionType != core.TaskApplicationCampaign || definitions[0].ProfileID != "primary" {
		t.Fatalf("definition=%#v", definitions[0])
	}
	if _, exists := routes["golang"]; !exists {
		t.Fatalf("campaign-owned routes=%#v", routes)
	}
	var payload core.ApplicationCampaignPayload
	if err := json.Unmarshal(definitions[0].Payload, &payload); err != nil || payload.Validate() != nil {
		t.Fatalf("campaign payload=%#v decode_err=%v validation_err=%v", payload, err, payload.Validate())
	}
}

func TestConfigureApplicationCampaignsRequiresEveryProfileAPIReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job-agent.db")
	if err := storesqlite.MigrateUp(path); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	store, err := storesqlite.Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reader := &profileReaderStub{}
	cfg := appconfig.Config{
		Searches: []appconfig.Search{{
			Tag: "golang", Adapter: "platform", Profiles: []string{"primary", "secondary"}, Query: json.RawMessage(`{}`),
		}},
		Jobs: []appconfig.Job{{
			Tag: "daily", Enabled: true, Concurrency: appconfig.JobConcurrencyForbid,
			Triggers: []appconfig.JobTrigger{{Type: "cron", Expression: "30 9 * * *", Timezone: "Europe/Moscow", Misfire: "run_once"}},
			Action: appconfig.JobAction{
				Type: appconfig.JobActionApplicationCampaign, Profiles: []string{"primary", "secondary"}, Routes: []string{"golang"},
				TargetSuccessful: 20, MaxInFlight: 3,
			},
		}},
	}
	_, definitions, _, jobs, err := configureApplicationCampaigns(
		cfg, map[string]adapter.Adapter{"platform": &profileAdapterStub{reader: reader}}, map[core.ProfileID]profileRuntime{
			"primary":   {Status: core.ProfileEnabled, Reader: reader},
			"secondary": {Status: core.ProfileEnabled},
		}, store,
	)
	if err != nil {
		t.Fatalf("configure campaigns: %v", err)
	}
	if jobs != 0 || len(definitions) != 0 {
		t.Fatalf("jobs=%d definitions=%d, want disabled campaign", jobs, len(definitions))
	}
}

func TestConfigureApplicationCampaignsAcceptsReadOnlyBrowserProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job-agent.db")
	if err := storesqlite.MigrateUp(path); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	store, err := storesqlite.Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reader := &profileAdapterStub{}
	cfg := appconfig.Config{
		Searches: []appconfig.Search{{
			Tag: "golang", Adapter: "platform", Profiles: []string{"primary"}, Query: json.RawMessage(`{}`),
		}},
		Jobs: []appconfig.Job{{
			Tag: "dry-run", Enabled: true, Concurrency: appconfig.JobConcurrencyForbid,
			Triggers: []appconfig.JobTrigger{{Type: "cron", Expression: "30 9 * * *", Timezone: "Europe/Moscow", Misfire: "run_once"}},
			Action: appconfig.JobAction{
				Type: appconfig.JobActionApplicationCampaign, Profiles: []string{"primary"}, Routes: []string{"golang"},
				TargetSuccessful: 1, MaxInFlight: 1,
			},
		}},
	}
	_, definitions, _, jobs, err := configureApplicationCampaigns(
		cfg, map[string]adapter.Adapter{"platform": reader}, map[core.ProfileID]profileRuntime{
			"primary": {Status: core.ProfileEnabled, BrowserReader: reader},
		}, store,
	)
	if err != nil {
		t.Fatalf("configure browser campaign: %v", err)
	}
	if jobs != 1 || len(definitions) != 1 {
		t.Fatalf("jobs=%d definitions=%d", jobs, len(definitions))
	}
}
