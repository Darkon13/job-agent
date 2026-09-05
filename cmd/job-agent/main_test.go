package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Darkon13/job-agent/adapter"
	appconfig "github.com/Darkon13/job-agent/config"
	"github.com/Darkon13/job-agent/core"
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

func (stub *profileAdapterStub) Name() string                         { return "stub" }
func (stub *profileAdapterStub) Capabilities() []core.Capability      { return nil }
func (stub *profileAdapterStub) ValidateSearch(json.RawMessage) error { return nil }
func (stub *profileAdapterStub) Search(context.Context, core.ProfileID, json.RawMessage, string) (core.SearchPage, error) {
	return core.SearchPage{}, errors.New("not implemented")
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
