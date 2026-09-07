package buildinfo

import (
	"bytes"
	"strings"
	"testing"
)

func TestCurrentUsesEmbeddedSemanticPrereleaseVersion(t *testing.T) {
	oldVersion, oldCommit, oldBuildTime, oldModified := Version, Commit, BuildTime, Modified
	t.Cleanup(func() { Version, Commit, BuildTime, Modified = oldVersion, oldCommit, oldBuildTime, oldModified })
	Version, Commit, BuildTime, Modified = "", "commit-1", "2026-09-07T12:00:00Z", "false"
	info := Current()
	if info.Version != strings.TrimSpace(defaultVersion) || info.APIVersion != "v1" || info.Commit != "commit-1" {
		t.Fatalf("build info = %#v", info)
	}
}

func TestWriteReportsComponentAndBuildIdentity(t *testing.T) {
	oldVersion, oldCommit, oldBuildTime, oldModified := Version, Commit, BuildTime, Modified
	t.Cleanup(func() { Version, Commit, BuildTime, Modified = oldVersion, oldCommit, oldBuildTime, oldModified })
	Version, Commit, BuildTime, Modified = "0.1.0", "abc", "2026-09-07T12:00:00Z", "false"
	var output bytes.Buffer
	if err := Write("job-agent", &output); err != nil {
		t.Fatalf("write version: %v", err)
	}
	for _, value := range []string{"job-agent 0.1.0", "api=v1", "commit=abc", "modified=false"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("output %q does not contain %q", output.String(), value)
		}
	}
}
