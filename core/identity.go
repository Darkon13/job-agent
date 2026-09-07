package core

import (
	"errors"
	"fmt"
	"strings"
)

type Platform string
type AdapterInstanceID string
type ProfileID string
type ProfileStateProposalID string
type SearchID string
type ApplicationID string
type ApplicationCampaignID string
type TaskID string
type EventID string
type CorrelationID string
type TestDefinitionID string
type ReviewSessionID string
type ReviewPromptID string
type QualificationID string
type ConversationID string
type MessageID string
type FollowUpID string
type ProfileActivityID string
type ProfileActivitySnapshotID string

// VacancyKey is the stable cross-search identity of a platform vacancy.
type VacancyKey struct {
	Platform   Platform `json:"platform"`
	ExternalID string   `json:"external_id"`
}

func (key VacancyKey) Validate() error {
	if strings.TrimSpace(string(key.Platform)) == "" {
		return errors.New("vacancy key requires platform")
	}
	if strings.TrimSpace(key.ExternalID) == "" {
		return errors.New("vacancy key requires external id")
	}
	return nil
}

func (key VacancyKey) String() string {
	return fmt.Sprintf("%s:%s", key.Platform, key.ExternalID)
}

// ApplicationKey enforces the uniqueness boundary profile/vacancy.
type ApplicationKey struct {
	ProfileID ProfileID  `json:"profile_id"`
	Vacancy   VacancyKey `json:"vacancy"`
}

func (key ApplicationKey) Validate() error {
	if strings.TrimSpace(string(key.ProfileID)) == "" {
		return errors.New("application key requires profile id")
	}
	if err := key.Vacancy.Validate(); err != nil {
		return fmt.Errorf("application key: %w", err)
	}
	return nil
}
