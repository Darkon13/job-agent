package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/credentials"
)

// LogoutTarget describes the secret artifacts of one profile. Empty references
// mean the profile has nothing to remove.
type LogoutTarget struct {
	ProfileID             core.ProfileID
	CredentialReference   string
	BrowserStateReference string
}

// LogoutResult reports which artifacts were present and removed.
type LogoutResult struct {
	CredentialRemoved   bool `json:"credential_removed"`
	BrowserStateRemoved bool `json:"browser_state_removed"`
}

// LogoutService removes stored credentials and browser state. It never touches
// platform sessions, so a later API call simply becomes unauthorized.
type LogoutService struct{}

func (service *LogoutService) Logout(ctx context.Context, target LogoutTarget) (LogoutResult, error) {
	if err := ctx.Err(); err != nil {
		return LogoutResult{}, err
	}
	if strings.TrimSpace(string(target.ProfileID)) == "" {
		return LogoutResult{}, errors.New("logout requires profile")
	}
	result := LogoutResult{}
	if reference := strings.TrimSpace(target.CredentialReference); reference != "" {
		if err := credentials.Delete(reference); err != nil {
			return LogoutResult{}, err
		}
		result.CredentialRemoved = true
	}
	if reference := strings.TrimSpace(target.BrowserStateReference); reference != "" {
		path := strings.TrimPrefix(reference, "file:")
		if strings.Contains(path, "://") {
			return LogoutResult{}, errors.New("browser state reference must be a file path")
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return LogoutResult{}, fmt.Errorf("remove browser state: %w", err)
		}
		result.BrowserStateRemoved = true
	}
	return result, nil
}
