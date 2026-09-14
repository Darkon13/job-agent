package operator

import "testing"

func TestParseResumeTailoringObjects(t *testing.T) {
	valid := map[string]string{
		"title":                             "title",
		"keySkills":                         "keySkills",
		"about":                             "about",
		"experience":                        "experience",
		"experience.2644170581":             "experience.2644170581",
		"experience.#0":                     "experience.#0",
		"experience.2644170581.description": "experience.2644170581.description",
		"experience.#2.description":         "experience.#2.description",
		"experience.order":                  "experience.order",
	}
	for value, expected := range valid {
		ref, err := ParseResumeTailoringObject(value)
		if err != nil {
			t.Fatalf("parse %q: %v", value, err)
		}
		if ref.String() != expected {
			t.Fatalf("parse %q = %q, want %q", value, ref.String(), expected)
		}
	}
	for _, value := range []string{"", "unknown", "experience.", "experience.x.y.z", "title.name", "experience.#x", "experience.#1.unknown"} {
		if _, err := ParseResumeTailoringObject(value); err == nil {
			t.Fatalf("expected %q to fail", value)
		}
	}
	if err := ValidateResumeTailoringWriteObject("skills", ResumeTailoringObjectReference{Name: "keySkills"}); err != nil {
		t.Fatalf("skills write: %v", err)
	}
	if err := ValidateResumeTailoringWriteObject("skills", ResumeTailoringObjectReference{Name: "title"}); err == nil {
		t.Fatal("skills must not write title")
	}
	order, _ := ParseResumeTailoringObject("experience.order")
	if err := ValidateResumeTailoringWriteObject("experience", order); err != nil {
		t.Fatalf("experience order write: %v", err)
	}
	block, _ := ParseResumeTailoringObject("experience.42.description")
	if err := ValidateResumeTailoringWriteObject("experience", block); err != nil {
		t.Fatalf("experience description write: %v", err)
	}
	if err := ValidateResumeTailoringWriteObject("experience", ResumeTailoringObjectReference{Name: "experience"}); err == nil {
		t.Fatal("bare experience must not be writable")
	}
}
