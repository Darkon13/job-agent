// Package credentials reads and writes platform credential records for the
// local runtime. Secret values stay out of errors, logs and diagnostics.
package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	FormatJSON   = "json"
	FormatDotenv = "dotenv"

	dotenvAccessTokenKey  = "HH_ACCESS_TOKEN"
	dotenvRefreshTokenKey = "HH_REFRESH_TOKEN"
	dotenvExpiresAtKey    = "HH_EXPIRES_AT"
)

var (
	// ErrUnsupportedReference reports a credential reference this runtime does
	// not understand.
	ErrUnsupportedReference = errors.New("unsupported credentials reference")
	// ErrReferenceUnavailable reports a missing or unreadable secret source.
	ErrReferenceUnavailable = errors.New("credentials reference is unavailable")
	// ErrUnsafePermissions reports a secret readable by group or others.
	ErrUnsafePermissions = errors.New("credentials file permissions must not allow group or other access")
	// ErrInvalidRecord reports malformed credential content.
	ErrInvalidRecord = errors.New("credentials record is invalid")
	// ErrSecretExists reports an existing credential output without overwrite.
	ErrSecretExists = errors.New("credential output already exists")
	// ErrSymlinkNotAllowed reports a credential output path that is a symlink.
	ErrSymlinkNotAllowed = errors.New("credential output must not be a symlink")
)

// Record is the canonical credential payload. Platform and ProfileID are
// optional so an imported access-token snapshot stays readable.
type Record struct {
	Platform     string     `json:"platform,omitempty"`
	ProfileID    string     `json:"profile_id,omitempty"`
	TokenType    string     `json:"token_type,omitempty"`
	AccessToken  string     `json:"access_token"`
	RefreshToken string     `json:"refresh_token,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	Scopes       []string   `json:"scopes,omitempty"`
	Revision     uint64     `json:"revision,omitempty"`
}

func (record Record) Validate() error {
	if strings.TrimSpace(record.AccessToken) == "" {
		return fmt.Errorf("%w: access token is required", ErrInvalidRecord)
	}
	return nil
}

// ParseFormat normalizes a credential output format. An empty value defaults to
// JSON, the primary machine format.
func ParseFormat(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", FormatJSON:
		return FormatJSON, nil
	case FormatDotenv:
		return FormatDotenv, nil
	default:
		return "", fmt.Errorf("%w: unknown credential format %q", ErrInvalidRecord, value)
	}
}

// Encode renders a record in the requested format. The result contains the
// secret and must be written only to a protected sink.
func Encode(record Record, format string) ([]byte, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	normalized, err := ParseFormat(format)
	if err != nil {
		return nil, err
	}
	switch normalized {
	case FormatJSON:
		encoded, err := json.Marshal(record)
		if err != nil {
			return nil, fmt.Errorf("encode credential record: %w", err)
		}
		return append(encoded, '\n'), nil
	case FormatDotenv:
		return encodeDotenv(record)
	default:
		return nil, fmt.Errorf("%w: unknown credential format %q", ErrInvalidRecord, format)
	}
}

func encodeDotenv(record Record) ([]byte, error) {
	var builder strings.Builder
	accessToken, err := dotenvValue(record.AccessToken)
	if err != nil {
		return nil, err
	}
	builder.WriteString(dotenvAccessTokenKey + "=" + accessToken + "\n")
	if record.RefreshToken != "" {
		refreshToken, err := dotenvValue(record.RefreshToken)
		if err != nil {
			return nil, err
		}
		builder.WriteString(dotenvRefreshTokenKey + "=" + refreshToken + "\n")
	}
	if record.ExpiresAt != nil {
		builder.WriteString(dotenvExpiresAtKey + "='" + record.ExpiresAt.UTC().Format(time.RFC3339) + "'\n")
	}
	return []byte(builder.String()), nil
}

func dotenvValue(value string) (string, error) {
	if strings.ContainsAny(value, "'\n\r\x00") {
		return "", fmt.Errorf("%w: value cannot be represented in dotenv", ErrInvalidRecord)
	}
	return "'" + value + "'", nil
}

// ParseDotenv decodes the dotenv representation produced by Encode. Unknown
// keys are ignored so the same file may carry unrelated environment values.
func ParseDotenv(data []byte) (Record, error) {
	values := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return Record{}, fmt.Errorf("%w: invalid dotenv line", ErrInvalidRecord)
		}
		values[strings.TrimSpace(key)] = unquoteDotenvValue(strings.TrimSpace(value))
	}
	record := Record{
		AccessToken:  values[dotenvAccessTokenKey],
		RefreshToken: values[dotenvRefreshTokenKey],
	}
	if raw := strings.TrimSpace(values[dotenvExpiresAtKey]); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return Record{}, fmt.Errorf("%w: invalid expires_at value", ErrInvalidRecord)
		}
		utc := parsed.UTC()
		record.ExpiresAt = &utc
	}
	if err := record.Validate(); err != nil {
		return Record{}, err
	}
	return record, nil
}

func unquoteDotenvValue(value string) string {
	if len(value) < 2 {
		return value
	}
	if (value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"') {
		return value[1 : len(value)-1]
	}
	return value
}
