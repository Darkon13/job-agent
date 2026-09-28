package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrJobFragmentNotFound reports a job that the dashboard does not manage.
var ErrJobFragmentNotFound = errors.New("job is not managed by the dashboard")

// JobFragmentValidator checks one raw job definition against the loaded config
// and fills the trigger defaults. The config package owns the schema, so the
// workflow stays free of it.
type JobFragmentValidator func(job json.RawMessage) error

// JobFragmentWorkflow stores dashboard-managed jobs as config fragments in the
// profile store directory: every job lives in its own file and the config
// watcher picks the change up without a restart.
type JobFragmentWorkflow struct {
	directory string
	validate  JobFragmentValidator
}

func NewJobFragmentWorkflow(directory string, validate JobFragmentValidator) (*JobFragmentWorkflow, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("job fragment workflow requires the store directory")
	}
	if validate == nil {
		return nil, errors.New("job fragment workflow requires a validator")
	}
	return &JobFragmentWorkflow{directory: filepath.Clean(directory), validate: validate}, nil
}

// Directory reports the effective fragment directory.
func (workflow *JobFragmentWorkflow) Directory() string {
	return workflow.directory
}

// Save validates the job and writes its fragment atomically. The tag comes from
// the job itself, so the file name always matches the definition.
func (workflow *JobFragmentWorkflow) Save(ctx context.Context, job json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(job) == 0 || !json.Valid(job) {
		return errors.New("job definition must be valid JSON")
	}
	var header struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(job, &header); err != nil {
		return fmt.Errorf("decode job tag: %w", err)
	}
	tag := strings.TrimSpace(header.Tag)
	if tag == "" {
		return errors.New("job definition requires a tag")
	}
	if !safeJobTag(tag) {
		return fmt.Errorf("job tag %q must use letters, digits, dot, dash or underscore", tag)
	}
	if err := workflow.validate(job); err != nil {
		return err
	}
	fragment := struct {
		Jobs []json.RawMessage `json:"jobs"`
	}{Jobs: []json.RawMessage{job}}
	data, err := json.MarshalIndent(fragment, "", "  ")
	if err != nil {
		return fmt.Errorf("encode job fragment: %w", err)
	}
	if err := writeFragmentFile(workflow.fragmentPath(tag), append(data, '\n')); err != nil {
		return err
	}
	return nil
}

// Delete removes the fragment of one dashboard-managed job.
func (workflow *JobFragmentWorkflow) Delete(ctx context.Context, tag string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return errors.New("job tag is required")
	}
	if !safeJobTag(tag) {
		return fmt.Errorf("job tag %q must use letters, digits, dot, dash or underscore", tag)
	}
	err := os.Remove(workflow.fragmentPath(tag))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%w: %s", ErrJobFragmentNotFound, tag)
	case err != nil:
		return fmt.Errorf("delete job fragment %s: %w", tag, err)
	}
	return nil
}

// Exists reports whether the dashboard manages the job with this tag.
func (workflow *JobFragmentWorkflow) Exists(tag string) bool {
	_, err := os.Stat(workflow.fragmentPath(strings.TrimSpace(tag)))
	return err == nil
}

func (workflow *JobFragmentWorkflow) fragmentPath(tag string) string {
	return filepath.Join(workflow.directory, "job-"+tag+".json")
}

// safeJobTag keeps the tag usable as a file name.
func safeJobTag(tag string) bool {
	for _, symbol := range tag {
		switch {
		case symbol >= 'a' && symbol <= 'z', symbol >= 'A' && symbol <= 'Z',
			symbol >= '0' && symbol <= '9', symbol == '.', symbol == '-', symbol == '_':
		default:
			return false
		}
	}
	return true
}

// writeFragmentFile stores the fragment atomically with owner-only permissions.
func writeFragmentFile(path string, data []byte) error {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		return nil
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read job fragment %s: %w", path, err)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create job fragment directory %s: %w", directory, err)
	}
	temp, err := os.CreateTemp(directory, ".job-fragment-*.json")
	if err != nil {
		return fmt.Errorf("create job fragment: %w", err)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write job fragment: %w", err)
	}
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("protect job fragment: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close job fragment: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("store job fragment %s: %w", path, err)
	}
	return nil
}
