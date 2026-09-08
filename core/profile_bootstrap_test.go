package core

import (
	"strings"
	"testing"
)

func TestDecodeProfileBootstrapManifestBuildsCanonicalResource(t *testing.T) {
	manifest, err := DecodeProfileBootstrapManifest([]byte(`{
		"api_version":"job-agent/v1",
		"kind":"ProfileBootstrap",
		"metadata":{"name":"primary-backend"},
		"spec":{"profile_id":"primary","state":{"resumes":{"resume-42":{
			"title":"Backend developer",
			"skill_set":["Go","PostgreSQL"],
			"experience":[{"company":"Example","position":"Developer"}]
		}}}}
	}`))
	if err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	resource, err := manifest.Resource()
	if err != nil {
		t.Fatalf("build resource: %v", err)
	}
	if resource.Tag != "primary-backend" || resource.ProfileID != "primary" || resource.Ownership != ProfileStateOwnershipDeclaredFields {
		t.Fatalf("resource = %#v", resource)
	}
	if strings.Contains(string(resource.State), "\n") || resource.ManifestDigest == "" {
		t.Fatalf("resource is not canonical: %s", resource.State)
	}
	paths, err := resource.DeclaredPaths()
	if err != nil {
		t.Fatalf("declared paths: %v", err)
	}
	want := []string{
		"/resumes/resume-42/experience",
		"/resumes/resume-42/skill_set",
		"/resumes/resume-42/title",
	}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
}

func TestDecodeProfileBootstrapManifestRejectsControlTyposAndInvalidShape(t *testing.T) {
	tests := []string{
		`{"api_version":"job-agent/v1","kind":"ProfileBootstrap","metadata":{"name":"resume","typo":true},"spec":{"profile_id":"primary","state":{"profile":{"area":"1"}}}}`,
		`{"api_version":"job-agent/v2","kind":"ProfileBootstrap","metadata":{"name":"resume"},"spec":{"profile_id":"primary","state":{"profile":{"area":"1"}}}}`,
		`{"api_version":"job-agent/v1","kind":"ProfileBootstrap","metadata":{"name":"resume"},"spec":{"profile_id":"","state":{"profile":{"area":"1"}}}}`,
		`{"api_version":"job-agent/v1","kind":"ProfileBootstrap","metadata":{"name":"resume"},"spec":{"profile_id":"primary","ownership":"everything","state":{"profile":{"area":"1"}}}}`,
		`{"api_version":"job-agent/v1","kind":"ProfileBootstrap","metadata":{"name":"resume"},"spec":{"profile_id":"primary","state":{}}}`,
	}
	for _, data := range tests {
		if _, err := DecodeProfileBootstrapManifest([]byte(data)); err == nil {
			t.Fatalf("expected manifest to fail: %s", data)
		}
	}
}
