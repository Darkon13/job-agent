package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// TestOption is the stable, platform-ID-independent representation retained in
// the test catalog. Keeping every option lets a user review an earlier choice.
type TestOption struct {
	Text string `json:"text"`
}

// TestQuestion is one progressively discovered question. Fingerprint covers
// the kind, normalized text, and every displayed option, but not runtime IDs.
type TestQuestion struct {
	Fingerprint string       `json:"fingerprint"`
	Text        string       `json:"text"`
	Kind        QuestionKind `json:"kind"`
	Options     []TestOption `json:"options,omitempty"`
	FirstSeenAt time.Time    `json:"first_seen_at"`
	LastSeenAt  time.Time    `json:"last_seen_at"`
}

// QualificationDescriptor groups platform-specific tests without assuming a
// fixed number or naming scheme for proficiency levels.
type QualificationDescriptor struct {
	FamilyID   string `json:"family_id"`
	FamilyName string `json:"family_name"`
	LevelID    string `json:"level_id"`
	LevelName  string `json:"level_name"`
	LevelOrder *int   `json:"level_order,omitempty"`
}

// TestDefinition is a growing catalog entry addressed by the platform handle.
// Questions become known only as attempts reveal them. LastAttemptFingerprint
// is diagnostic evidence, not the key used to answer the next question.
type TestDefinition struct {
	ID                     TestDefinitionID         `json:"id"`
	Platform               Platform                 `json:"platform"`
	ExternalID             string                   `json:"external_id"`
	Title                  string                   `json:"title,omitempty"`
	Qualification          *QualificationDescriptor `json:"qualification,omitempty"`
	Questions              []TestQuestion           `json:"questions"`
	LastAttemptFingerprint string                   `json:"last_attempt_fingerprint,omitempty"`
	ObservedAttempts       uint64                   `json:"observed_attempts"`
	DiscoveredAt           time.Time                `json:"discovered_at"`
	UpdatedAt              time.Time                `json:"updated_at"`
}

// NewProgressiveTestDefinition creates a catalog entry before an attempt starts
// and therefore does not require any questions to be known.
func NewProgressiveTestDefinition(platform Platform, externalID, title string, qualification *QualificationDescriptor, now time.Time) (TestDefinition, error) {
	if platform == "" || strings.TrimSpace(externalID) == "" {
		return TestDefinition{}, errors.New("test definition requires platform and external id")
	}
	if now.IsZero() {
		return TestDefinition{}, errors.New("test definition requires current time")
	}
	if qualification != nil {
		if err := validateQualificationDescriptor(*qualification); err != nil {
			return TestDefinition{}, err
		}
	}

	idParts := []string{string(platform)}
	if qualification != nil {
		idParts = append(idParts, qualification.FamilyID, qualification.LevelID)
	}
	idParts = append(idParts, externalID)
	definition := TestDefinition{
		ID: TestDefinitionID(strings.Join(idParts, ":")), Platform: platform,
		ExternalID: externalID, Title: title, Questions: []TestQuestion{},
		DiscoveredAt: now, UpdatedAt: now,
	}
	if qualification != nil {
		value := cloneQualificationDescriptor(*qualification)
		definition.Qualification = &value
	}
	return definition, nil
}

// ObserveQuestion adds a newly revealed question or updates its last-seen time.
// A changed option produces another fingerprint and is retained as a revision.
func (definition *TestDefinition) ObserveQuestion(question Question, now time.Time) (string, bool, error) {
	if definition == nil {
		return "", false, errors.New("test definition is nil")
	}
	if now.IsZero() || now.Before(definition.UpdatedAt) {
		return "", false, errors.New("question observation time must not move backwards")
	}
	fingerprint, err := QuestionFingerprint(question)
	if err != nil {
		return "", false, err
	}
	for index := range definition.Questions {
		if definition.Questions[index].Fingerprint == fingerprint {
			definition.Questions[index].LastSeenAt = now
			definition.UpdatedAt = now
			return fingerprint, false, nil
		}
	}

	stored := TestQuestion{
		Fingerprint: fingerprint, Text: question.Text, Kind: question.Kind,
		FirstSeenAt: now, LastSeenAt: now,
	}
	for _, option := range question.Options {
		stored.Options = append(stored.Options, TestOption{Text: option.Text})
	}
	sort.Slice(stored.Options, func(i, j int) bool {
		return NormalizeQuestionText(stored.Options[i].Text) < NormalizeQuestionText(stored.Options[j].Text)
	})
	definition.Questions = append(definition.Questions, stored)
	sort.Slice(definition.Questions, func(i, j int) bool {
		return definition.Questions[i].Fingerprint < definition.Questions[j].Fingerprint
	})
	definition.UpdatedAt = now
	return fingerprint, true, nil
}

// CompleteObservedAttempt records only the questions seen on this attempt. It
// does not claim that the platform has no other questions in its pool.
func (definition *TestDefinition) CompleteObservedAttempt(questionFingerprints []string, now time.Time) (string, error) {
	if definition == nil {
		return "", errors.New("test definition is nil")
	}
	if len(questionFingerprints) == 0 {
		return "", errors.New("observed attempt requires question fingerprints")
	}
	if now.IsZero() || now.Before(definition.UpdatedAt) {
		return "", errors.New("attempt completion time must not move backwards")
	}
	known := make(map[string]struct{}, len(definition.Questions))
	for _, question := range definition.Questions {
		known[question.Fingerprint] = struct{}{}
	}
	seen := make(map[string]struct{}, len(questionFingerprints))
	manifest := append([]string(nil), questionFingerprints...)
	for _, fingerprint := range manifest {
		if err := validateSHA256Fingerprint(fingerprint); err != nil {
			return "", err
		}
		if _, exists := known[fingerprint]; !exists {
			return "", fmt.Errorf("attempt references unknown question fingerprint %s", fingerprint)
		}
		if _, exists := seen[fingerprint]; exists {
			return "", fmt.Errorf("attempt contains duplicate question fingerprint %s", fingerprint)
		}
		seen[fingerprint] = struct{}{}
	}
	sort.Strings(manifest)
	sum := sha256.Sum256([]byte(strings.Join(manifest, "\x1d")))
	fingerprint := hex.EncodeToString(sum[:])
	definition.LastAttemptFingerprint = fingerprint
	definition.ObservedAttempts++
	definition.UpdatedAt = now
	return fingerprint, nil
}

// BuildTestDefinition is a convenience for static mocks/imports that already
// expose a complete observed attempt.
func BuildTestDefinition(platform Platform, questionnaire Questionnaire, now time.Time) (TestDefinition, error) {
	return BuildQualificationTestDefinition(platform, questionnaire, nil, now)
}

func BuildQualificationTestDefinition(platform Platform, questionnaire Questionnaire, qualification *QualificationDescriptor, now time.Time) (TestDefinition, error) {
	attemptFingerprint, err := ObservedAttemptFingerprint(questionnaire)
	if err != nil {
		return TestDefinition{}, err
	}
	definition, err := NewProgressiveTestDefinition(platform, "observed:"+attemptFingerprint, questionnaire.Title, qualification, now)
	if err != nil {
		return TestDefinition{}, err
	}
	fingerprints := make([]string, 0, len(questionnaire.Questions))
	for _, question := range questionnaire.Questions {
		fingerprint, _, err := definition.ObserveQuestion(question, now)
		if err != nil {
			return TestDefinition{}, err
		}
		fingerprints = append(fingerprints, fingerprint)
	}
	if _, err := definition.CompleteObservedAttempt(fingerprints, now); err != nil {
		return TestDefinition{}, err
	}
	return definition, nil
}

func validateQualificationDescriptor(qualification QualificationDescriptor) error {
	if qualification.FamilyID == "" || qualification.FamilyName == "" || qualification.LevelID == "" || qualification.LevelName == "" {
		return errors.New("qualification descriptor requires family and level ids and names")
	}
	if qualification.LevelOrder != nil && *qualification.LevelOrder < 0 {
		return errors.New("qualification level order must not be negative")
	}
	return nil
}

func cloneQualificationDescriptor(source QualificationDescriptor) QualificationDescriptor {
	result := source
	if source.LevelOrder != nil {
		order := *source.LevelOrder
		result.LevelOrder = &order
	}
	return result
}
