package core

import (
	"fmt"
	"sort"
)

// Capability describes an operation exposed by a platform adapter.
type Capability string

const (
	CapabilitySearchVacancies        Capability = "vacancies.search"
	CapabilityGetVacancy             Capability = "vacancies.get"
	CapabilityInspectVacancy         Capability = "vacancies.inspect"
	CapabilityApply                  Capability = "applications.apply"
	CapabilityQuestionnaire          Capability = "applications.questionnaire"
	CapabilityConversationRead       Capability = "conversations.read"
	CapabilityConversationWrite      Capability = "conversations.write"
	CapabilityConversationMarkRead   Capability = "conversations.mark_read"
	CapabilityResumeRead             Capability = "resumes.read"
	CapabilityResumeCreate           Capability = "resumes.create"
	CapabilityResumeUpdate           Capability = "resumes.update"
	CapabilityResumePublish          Capability = "resumes.publish"
	CapabilityResumeTouch            Capability = "resumes.touch"
	CapabilityProfileActivityObserve Capability = "profile.activity.observe"
	CapabilitySkillVerificationRead  Capability = "skill_verifications.read"
	CapabilitySkillVerificationStart Capability = "skill_verifications.start"
)

// CapabilitySet is immutable by convention after construction. It provides a
// deterministic representation for API responses and diagnostics.
type CapabilitySet map[Capability]struct{}

func NewCapabilitySet(capabilities ...Capability) (CapabilitySet, error) {
	set := make(CapabilitySet, len(capabilities))
	for _, capability := range capabilities {
		if capability == "" {
			return nil, fmt.Errorf("capability must not be empty")
		}
		set[capability] = struct{}{}
	}
	return set, nil
}

func (s CapabilitySet) Supports(capability Capability) bool {
	_, exists := s[capability]
	return exists
}

func (s CapabilitySet) Sorted() []Capability {
	result := make([]Capability, 0, len(s))
	for capability := range s {
		result = append(result, capability)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
