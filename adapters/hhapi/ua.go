package hhapi

import (
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	mrand "math/rand"
	"os"
	"path/filepath"
	"strings"
)

// mobileModels mirrors the Android devices used by hh-applicant-tool. A fresh
// installation gets one generated identity; the file keeps the backend,
// dashboard captcha presenter and batch on the same device identity.
var mobileModels = []string{
	"23053RN02A", "23053RN02Y", "23053RN02I", "23053RN02L", "23077RABDC",
	"2411DRN47C", "2409BRN2CA", "2409BRN2CG", "2409BRN2CY",
	"2508CRN2BE", "2508CRN2BC", "2508CRN2BG",
	"SM-A165F", "SM-A165F/DS", "SM-A165M", "SM-A165M/DS", "SM-A165F/DSB",
	"24108PCE2I", "MZB0KE1IN",
}

func generateAndroidUserAgent() string {
	model := mobileModels[mrand.Intn(len(mobileModels))]
	minor := 100 + mrand.Intn(51)
	patch := 10000 + mrand.Intn(5001)
	android := 11 + mrand.Intn(5)
	id := make([]byte, 16)
	_, _ = crand.Read(id)
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	uuid := fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(id[0:4]), hex.EncodeToString(id[4:6]),
		hex.EncodeToString(id[6:8]), hex.EncodeToString(id[8:10]),
		hex.EncodeToString(id[10:16]))
	return fmt.Sprintf("ru.hh.android/7.%d.%d, Device: %s, Android OS: %d (UUID: %s)", minor, patch, model, android, uuid)
}

func userAgentFilePath() string {
	if path := strings.TrimSpace(os.Getenv("HH_USER_AGENT_FILE")); path != "" {
		return path
	}
	cookies := cookieFilePath()
	if cookies == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(cookies), "hh-user-agent.txt")
}

// deviceUserAgent returns the shared mobile identity. The backend rotates it on
// startup, while the dashboard and batch read the same file for captcha.
func deviceUserAgent() string {
	if value := strings.TrimSpace(os.Getenv("HH_USER_AGENT")); value != "" {
		return value
	}
	path := userAgentFilePath()
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			if value := strings.TrimSpace(string(data)); value != "" {
				return value
			}
		}
	}
	value := generateAndroidUserAgent()
	if path != "" {
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		_ = os.WriteFile(path, []byte(value), 0o600)
	}
	return value
}

func rotateDeviceUserAgent() string {
	if value := strings.TrimSpace(os.Getenv("HH_USER_AGENT")); value != "" {
		return value
	}
	value := generateAndroidUserAgent()
	if path := userAgentFilePath(); path != "" {
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		_ = os.WriteFile(path, []byte(value), 0o600)
	}
	return value
}
