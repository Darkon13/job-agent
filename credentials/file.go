package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maximumRecordBytes = 64 << 10

// Reference is a parsed credentials_ref. The canonical forms are
// file:/path/credentials.json and dotenv-file:/path/credentials.env; a bare
// path keeps working as JSON.
type Reference struct {
	Scheme string
	Path   string
}

func ParseReference(value string) (Reference, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return Reference{}, fmt.Errorf("%w: empty reference", ErrUnsupportedReference)
	}
	switch {
	case strings.HasPrefix(trimmed, "dotenv-file:"):
		return newReference("dotenv-file", strings.TrimPrefix(trimmed, "dotenv-file:"))
	case strings.HasPrefix(trimmed, "file:"):
		return newReference("file", strings.TrimPrefix(trimmed, "file:"))
	case strings.Contains(trimmed, "://"):
		return Reference{}, fmt.Errorf("%w: unsupported scheme", ErrUnsupportedReference)
	default:
		return newReference("file", trimmed)
	}
}

func newReference(scheme, path string) (Reference, error) {
	path = strings.TrimSpace(path)
	if path == "" || strings.Contains(path, "://") {
		return Reference{}, fmt.Errorf("%w: invalid path", ErrUnsupportedReference)
	}
	return Reference{Scheme: scheme, Path: path}, nil
}

// Load reads one credential record from a JSON or dotenv reference.
func Load(value string) (Record, error) {
	reference, err := ParseReference(value)
	if err != nil {
		return Record{}, err
	}
	data, err := readSecretFile(reference.Path)
	if err != nil {
		return Record{}, err
	}
	switch reference.Scheme {
	case "file":
		var record Record
		if err := json.Unmarshal(data, &record); err != nil {
			return Record{}, fmt.Errorf("%w: credential file is not valid JSON", ErrInvalidRecord)
		}
		if err := record.Validate(); err != nil {
			return Record{}, err
		}
		return record, nil
	case "dotenv-file":
		return ParseDotenv(data)
	default:
		return Record{}, fmt.Errorf("%w: unsupported scheme", ErrUnsupportedReference)
	}
}

func readSecretFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: credential file cannot be opened", ErrReferenceUnavailable)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: credential file cannot be inspected", ErrReferenceUnavailable)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: credential reference is not a regular file", ErrReferenceUnavailable)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, ErrUnsafePermissions
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumRecordBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: credential file cannot be read", ErrReferenceUnavailable)
	}
	if len(data) > maximumRecordBytes {
		return nil, fmt.Errorf("%w: credential file is too large", ErrInvalidRecord)
	}
	return data, nil
}

// WriteFile atomically stores a credential record with 0600 permissions. It
// never follows a symlink and refuses to replace an existing secret unless
// force is set.
func WriteFile(path string, record Record, format string, force bool) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return fmt.Errorf("%w: empty output path", ErrInvalidRecord)
	}
	data, err := Encode(record, format)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(trimmed); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrSymlinkNotAllowed
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: output path is not a regular file", ErrInvalidRecord)
		}
		if !force {
			return ErrSecretExists
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect credential output: %w", err)
	}
	directory := filepath.Dir(trimmed)
	temp, err := os.CreateTemp(directory, "."+filepath.Base(trimmed)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create credential output: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("protect credential output: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write credential output: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("flush credential output: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close credential output: %w", err)
	}
	if err := os.Rename(tempName, trimmed); err != nil {
		return fmt.Errorf("replace credential output: %w", err)
	}
	return nil
}
