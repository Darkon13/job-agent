package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Darkon13/job-agent/core"
)

func loadProfileConfig(t *testing.T, profile string) (Config, error) {
	t.Helper()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	writeConfigTestFile(t, configPath, `{
		"schema_version":1,
		"database":{"driver":"sqlite","path":"job-agent.db"},
		"adapters":[{"tag":"hh-main","type":"hh"}],
		"profiles":[`+profile+`]
	}`)
	return Load(configPath)
}

func TestResumeListIsCanonical(t *testing.T) {
	loaded, err := loadProfileConfig(t, `{"tag":"primary","adapter":"hh-main","enabled":false,
		"resume":"golang",
		"resumes":[{"id":"backend"},{"id":"golang","title":"Go developer","primary":true}],
		"resume_aliases":{"be":"backend","go":"golang"}}`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	profile := loaded.Profiles[0]
	ids := profile.ResumeIDs()
	if len(ids) != 2 || ids[0] != "backend" || ids[1] != "golang" {
		t.Fatalf("resume ids = %#v", ids)
	}
	if profile.PrimaryResumeID() != "golang" || profile.Resume != "golang" {
		t.Fatalf("primary = %q profile resume = %q", profile.PrimaryResumeID(), profile.Resume)
	}
	targets := loaded.ResumeTargets()[core.ProfileID("primary")]
	if len(targets) != 2 {
		t.Fatalf("targets = %#v", targets)
	}
	if targets[0].ID != "backend" || targets[0].Primary || targets[0].Alias != "be" {
		t.Fatalf("targets = %#v", targets)
	}
	if targets[1].ID != "golang" || !targets[1].Primary || targets[1].Title != "Go developer" || targets[1].Alias != "go" {
		t.Fatalf("targets = %#v", targets)
	}
}

func TestResumeListDefaultsToTheFirstEntry(t *testing.T) {
	loaded, err := loadProfileConfig(t, `{"tag":"primary","adapter":"hh-main","enabled":false,
		"resumes":[{"id":"resume-1"},{"id":"resume-2"}]}`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Profiles[0].PrimaryResumeID() != "resume-1" {
		t.Fatalf("primary = %q", loaded.Profiles[0].PrimaryResumeID())
	}
}

func TestResumeListRejectsInconsistentDeclarations(t *testing.T) {
	cases := map[string]string{
		"duplicate id": `{"tag":"primary","adapter":"hh-main","enabled":false,
			"resumes":[{"id":"resume-1"},{"id":"resume-1"}]}`,
		"two primaries": `{"tag":"primary","adapter":"hh-main","enabled":false,
			"resumes":[{"id":"resume-1","primary":true},{"id":"resume-2","primary":true}]}`,
		"alias outside the list": `{"tag":"primary","adapter":"hh-main","enabled":false,
			"resumes":[{"id":"resume-1"}],"resume_aliases":{"backend":"resume-2"}}`,
		"legacy resume outside the list": `{"tag":"primary","adapter":"hh-main","enabled":false,
			"resume":"resume-2","resumes":[{"id":"resume-1"}]}`,
		"empty resume id": `{"tag":"primary","adapter":"hh-main","enabled":false,
			"resumes":[{"id":""}]}`,
	}
	for name, profile := range cases {
		if _, err := loadProfileConfig(t, profile); err == nil {
			t.Fatalf("%s: expected an error", name)
		} else if !strings.Contains(err.Error(), "resume") {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
}
