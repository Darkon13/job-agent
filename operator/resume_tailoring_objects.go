package operator

import (
	"fmt"
	"strconv"
	"strings"
)

// ResumeTailoringObjectReference names one resume object an operator may read
// or write. Objects are declared in config instead of JSON Pointers. Work
// experience blocks are addressed explicitly because a resume has many of them:
// experience.<id> refers to a platform block id and experience.#N to its
// position when the platform hides ids.
type ResumeTailoringObjectReference struct {
	Name       string
	EntryID    string
	EntryIndex int
	HasIndex   bool
	Field      string
}

func (ref ResumeTailoringObjectReference) String() string {
	if ref.Name != "experience" {
		return ref.Name
	}
	if ref.Field == "order" {
		return "experience.order"
	}
	if ref.EntryID == "" && !ref.HasIndex {
		return "experience"
	}
	entry := ref.EntryID
	if ref.HasIndex {
		entry = "#" + strconv.Itoa(ref.EntryIndex)
	}
	if ref.Field == "" {
		return "experience." + entry
	}
	return "experience." + entry + "." + ref.Field
}

// ParseResumeTailoringObject parses one declared object reference.
func ParseResumeTailoringObject(value string) (ResumeTailoringObjectReference, error) {
	trimmed := strings.TrimSpace(value)
	parts := strings.Split(trimmed, ".")
	if len(parts) == 0 || trimmed == "" {
		return ResumeTailoringObjectReference{}, fmt.Errorf("resume tailoring object is empty")
	}
	name := parts[0]
	switch name {
	case "title", "about", "keySkills", "education", "languages":
		if len(parts) != 1 {
			return ResumeTailoringObjectReference{}, fmt.Errorf("resume tailoring object %q does not take a field", value)
		}
		return ResumeTailoringObjectReference{Name: name}, nil
	case "experience":
		switch len(parts) {
		case 1:
			return ResumeTailoringObjectReference{Name: name}, nil
		case 2:
			if parts[1] == "order" {
				return ResumeTailoringObjectReference{Name: "experience", Field: "order"}, nil
			}
			entry, err := parseExperienceObjectEntry(parts[1])
			if err != nil {
				return ResumeTailoringObjectReference{}, fmt.Errorf("resume tailoring object %q: %w", value, err)
			}
			return entry, nil
		case 3:
			entry, err := parseExperienceObjectEntry(parts[1])
			if err != nil {
				return ResumeTailoringObjectReference{}, fmt.Errorf("resume tailoring object %q: %w", value, err)
			}
			if parts[2] != "description" {
				return ResumeTailoringObjectReference{}, fmt.Errorf("resume tailoring object %q has unsupported field %q", value, parts[2])
			}
			entry.Field = "description"
			return entry, nil
		default:
			return ResumeTailoringObjectReference{}, fmt.Errorf("resume tailoring object %q is too deep", value)
		}
	default:
		return ResumeTailoringObjectReference{}, fmt.Errorf("resume tailoring object %q is unknown", value)
	}
}

func parseExperienceObjectEntry(value string) (ResumeTailoringObjectReference, error) {
	ref := ResumeTailoringObjectReference{Name: "experience"}
	if strings.HasPrefix(value, "#") {
		index, err := strconv.Atoi(strings.TrimPrefix(value, "#"))
		if err != nil || index < 0 {
			return ResumeTailoringObjectReference{}, fmt.Errorf("experience position %q must be a non-negative number", value)
		}
		ref.HasIndex = true
		ref.EntryIndex = index
		return ref, nil
	}
	if strings.TrimSpace(value) == "" {
		return ResumeTailoringObjectReference{}, fmt.Errorf("experience block needs an id or #position")
	}
	ref.EntryID = value
	return ref, nil
}

// ValidateResumeTailoringWriteObject rejects objects a processor cannot change.
func ValidateResumeTailoringWriteObject(processor string, ref ResumeTailoringObjectReference) error {
	switch processor {
	case "skills":
		if ref.Name != "keySkills" {
			return fmt.Errorf("skills tailoring can only write keySkills, got %q", ref.String())
		}
	case "about":
		if ref.Name != "about" {
			return fmt.Errorf("about tailoring can only write about, got %q", ref.String())
		}
	case "experience":
		if ref.Name != "experience" {
			return fmt.Errorf("experience tailoring can only write experience objects, got %q", ref.String())
		}
		if ref.Field == "order" {
			return nil
		}
		if ref.Field != "description" || (ref.EntryID == "" && !ref.HasIndex) {
			return fmt.Errorf("experience tailoring can only write experience.<block>.description or experience.order, got %q", ref.String())
		}
	default:
		return fmt.Errorf("unknown resume tailoring processor %q", processor)
	}
	return nil
}

// ValidateResumeTailoringReadObject rejects unknown read objects.
func ValidateResumeTailoringReadObject(ref ResumeTailoringObjectReference) error {
	switch ref.Name {
	case "title", "about", "keySkills", "education", "languages":
		return nil
	case "experience":
		if ref.Field != "" {
			return fmt.Errorf("read object %q cannot select a field", ref.String())
		}
		return nil
	default:
		return fmt.Errorf("resume tailoring object %q is unknown", ref.String())
	}
}

// ResumeTailoringObjectPath maps an object reference to the HH browser profile
// path. Every experience reference shares the experience array path; the
// selection of blocks happens in the processor and never leaks into config.
func ResumeTailoringObjectPath(resumeID string, ref ResumeTailoringObjectReference) string {
	escaped := escapeResumeTailoringPointer(strings.TrimSpace(resumeID))
	switch ref.Name {
	case "title":
		return "/resumes/" + escaped + "/web/title"
	case "about":
		return "/resumes/" + escaped + "/web/skills"
	case "keySkills":
		return "/resumes/" + escaped + "/web/keySkills"
	case "education":
		return "/resumes/" + escaped + "/web_profile/primaryEducation"
	case "languages":
		return "/resumes/" + escaped + "/web_profile/language"
	default:
		return "/resumes/" + escaped + "/web_profile/experience"
	}
}

// ResumeTailoringObjectMatchesEntry reports whether a declared experience
// reference selects the observed block with the given id and position.
func ResumeTailoringObjectMatchesEntry(ref ResumeTailoringObjectReference, entryID string, index int) bool {
	if ref.Name != "experience" {
		return false
	}
	if ref.HasIndex {
		return ref.EntryIndex == index
	}
	return ref.EntryID != "" && ref.EntryID == entryID
}
