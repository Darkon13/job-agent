package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLogoutRemovesLocalSecrets(t *testing.T) {
	directory := t.TempDir()
	credentialPath := filepath.Join(directory, "credentials.json")
	statePath := filepath.Join(directory, "primary.state.json")
	if err := os.WriteFile(credentialPath, []byte(`{"access_token":"secret"}`), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	if err := os.WriteFile(statePath, []byte(`{"cookies":[]}`), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	service := &LogoutService{}
	result, err := service.Logout(context.Background(), LogoutTarget{
		ProfileID: "primary", CredentialReference: "file:" + credentialPath, BrowserStateReference: statePath,
	})
	if err != nil || !result.CredentialRemoved || !result.BrowserStateRemoved {
		t.Fatalf("result = %#v err=%v", result, err)
	}
	for _, path := range []string{credentialPath, statePath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists", path)
		}
	}
	repeated, err := service.Logout(context.Background(), LogoutTarget{
		ProfileID: "primary", CredentialReference: "file:" + credentialPath, BrowserStateReference: statePath,
	})
	if err != nil || !repeated.CredentialRemoved {
		t.Fatalf("repeated logout: %#v err=%v", repeated, err)
	}
	empty, err := service.Logout(context.Background(), LogoutTarget{ProfileID: "primary"})
	if err != nil || empty.CredentialRemoved || empty.BrowserStateRemoved {
		t.Fatalf("empty logout: %#v err=%v", empty, err)
	}
	if _, err := service.Logout(context.Background(), LogoutTarget{}); err == nil {
		t.Fatal("expected logout without profile to fail")
	}
}
