package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestJobFragmentWorkflowWritesAndDeletesFragments(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "profile-store")
	var validated json.RawMessage
	workflow, err := NewJobFragmentWorkflow(directory, func(raw json.RawMessage) error {
		validated = raw
		return nil
	})
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	job := json.RawMessage(`{"tag":"hourly-applications","enabled":true,"triggers":[{"type":"cron","expression":"0 * * * *"}]}`)
	if err := workflow.Save(ctx, job); err != nil {
		t.Fatalf("save: %v", err)
	}
	if string(validated) != string(job) {
		t.Fatalf("validator saw %s", validated)
	}
	path := filepath.Join(directory, "job-hourly-applications.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fragment: %v", err)
	}
	var fragment struct {
		Jobs []json.RawMessage `json:"jobs"`
	}
	if err := json.Unmarshal(data, &fragment); err != nil || len(fragment.Jobs) != 1 {
		t.Fatalf("fragment = %s err=%v", data, err)
	}
	if !workflow.Exists("hourly-applications") {
		t.Fatal("the saved job must exist")
	}
	// Repeating the same save is idempotent.
	if err := workflow.Save(ctx, job); err != nil {
		t.Fatalf("repeat save: %v", err)
	}
	// A validator failure leaves no file behind.
	broken, err := NewJobFragmentWorkflow(directory, func(json.RawMessage) error {
		return errors.New("profile \"primary\" is unknown")
	})
	if err != nil {
		t.Fatalf("new broken workflow: %v", err)
	}
	if err := broken.Save(ctx, json.RawMessage(`{"tag":"broken"}`)); err == nil {
		t.Fatal("expected validation to fail")
	}
	if _, err := os.Stat(filepath.Join(directory, "job-broken.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid job must not be stored: %v", err)
	}
	// Tags that cannot become file names are rejected.
	if err := workflow.Save(ctx, json.RawMessage(`{"tag":"../escape"}`)); err == nil {
		t.Fatal("expected an unsafe tag to fail")
	}
	if err := workflow.Delete(ctx, "hourly-applications"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := workflow.Delete(ctx, "hourly-applications"); !errors.Is(err, ErrJobFragmentNotFound) {
		t.Fatalf("second delete error = %v", err)
	}
}

func TestJobFragmentWorkflowExistsRejectsTraversalTags(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "profile-store")
	workflow, err := NewJobFragmentWorkflow(directory, func(json.RawMessage) error { return nil })
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create store: %v", err)
	}
	// A file one level above the store must stay invisible to Exists.
	outside := filepath.Join(filepath.Dir(directory), "secret.json")
	if err := os.WriteFile(outside, []byte("{}"), 0o600); err != nil {
		t.Fatalf("plant outside file: %v", err)
	}
	for _, tag := range []string{"../../../secret", "../escape", "..", "a/b", `a\b`, ""} {
		if workflow.Exists(tag) {
			t.Fatalf("Exists(%q) must be false", tag)
		}
	}
}
