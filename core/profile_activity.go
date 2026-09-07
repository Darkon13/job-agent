package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// ProfileActivityKind describes an observed, useful applicant action. These
// records are evidence, not an attempt to reproduce an undocumented platform
// score or assign guessed weights to actions.
type ProfileActivityKind string

const (
	ProfileActivityVacancyInspected        ProfileActivityKind = "vacancy.inspected"
	ProfileActivityApplicationSubmitted    ProfileActivityKind = "application.submitted"
	ProfileActivityConversationMessageSent ProfileActivityKind = "conversation.message_sent"
	ProfileActivityResumeTouched           ProfileActivityKind = "resume.touched"
)

type ProfileActivityRecord struct {
	ID         ProfileActivityID   `json:"id"`
	Platform   Platform            `json:"platform"`
	ProfileID  ProfileID           `json:"profile_id"`
	ResumeID   string              `json:"resume_id,omitempty"`
	Kind       ProfileActivityKind `json:"kind"`
	SourceID   string              `json:"source_id"`
	OccurredAt time.Time           `json:"occurred_at"`
}

// ProfileActivitySnapshot is a point-in-time observation returned by the
// platform. Pointer counters distinguish a real zero from a metric that the
// current UI/API did not expose.
type ProfileActivitySnapshot struct {
	ID                ProfileActivitySnapshotID `json:"id"`
	Platform          Platform                  `json:"platform"`
	ProfileID         ProfileID                 `json:"profile_id"`
	ResumeID          string                    `json:"resume_id"`
	SourceID          string                    `json:"-"`
	Score             *int                      `json:"score,omitempty"`
	ScoreHidden       bool                      `json:"score_hidden"`
	PeriodDays        *int                      `json:"period_days,omitempty"`
	SearchShows       *int                      `json:"search_shows,omitempty"`
	Views             *int                      `json:"views,omitempty"`
	NewViews          *int                      `json:"new_views,omitempty"`
	Invitations       *int                      `json:"invitations,omitempty"`
	NewInvitations    *int                      `json:"new_invitations,omitempty"`
	ResponseStreak    *int                      `json:"response_streak,omitempty"`
	ResponsesRequired *int                      `json:"responses_required,omitempty"`
	ObservedAt        time.Time                 `json:"observed_at"`
}

func NewProfileActivityRecord(platform Platform, profileID ProfileID, resumeID string, kind ProfileActivityKind, sourceID string, occurredAt time.Time) (ProfileActivityRecord, error) {
	platform = Platform(strings.TrimSpace(string(platform)))
	profileID = ProfileID(strings.TrimSpace(string(profileID)))
	resumeID = strings.TrimSpace(resumeID)
	sourceID = strings.TrimSpace(sourceID)
	if platform == "" || profileID == "" || sourceID == "" {
		return ProfileActivityRecord{}, errors.New("profile activity requires platform, profile and source id")
	}
	if occurredAt.IsZero() {
		return ProfileActivityRecord{}, errors.New("profile activity requires occurred_at")
	}
	switch kind {
	case ProfileActivityVacancyInspected, ProfileActivityApplicationSubmitted,
		ProfileActivityConversationMessageSent, ProfileActivityResumeTouched:
	default:
		return ProfileActivityRecord{}, errors.New("profile activity has invalid kind")
	}
	if kind == ProfileActivityResumeTouched && resumeID == "" {
		return ProfileActivityRecord{}, errors.New("resume touch activity requires resume id")
	}
	digest := sha256.Sum256([]byte(string(platform) + "\x00" + string(profileID) + "\x00" + string(kind) + "\x00" + sourceID))
	return ProfileActivityRecord{
		ID:       ProfileActivityID("activity-" + hex.EncodeToString(digest[:16])),
		Platform: platform, ProfileID: profileID, ResumeID: resumeID,
		Kind: kind, SourceID: sourceID, OccurredAt: occurredAt.UTC(),
	}, nil
}

func (record ProfileActivityRecord) Validate() error {
	if record.ID == "" {
		return errors.New("profile activity requires id")
	}
	expected, err := NewProfileActivityRecord(record.Platform, record.ProfileID, record.ResumeID, record.Kind, record.SourceID, record.OccurredAt)
	if err != nil {
		return err
	}
	if expected.ID != record.ID {
		return errors.New("profile activity id does not match its identity")
	}
	return nil
}

func NewProfileActivitySnapshot(platform Platform, profileID ProfileID, resumeID, sourceID string, observedAt time.Time) (ProfileActivitySnapshot, error) {
	platform = Platform(strings.TrimSpace(string(platform)))
	profileID = ProfileID(strings.TrimSpace(string(profileID)))
	resumeID = strings.TrimSpace(resumeID)
	sourceID = strings.TrimSpace(sourceID)
	if platform == "" || profileID == "" || resumeID == "" || sourceID == "" {
		return ProfileActivitySnapshot{}, errors.New("profile activity snapshot requires platform, profile, resume and source id")
	}
	if observedAt.IsZero() {
		return ProfileActivitySnapshot{}, errors.New("profile activity snapshot requires observed_at")
	}
	digest := sha256.Sum256([]byte(string(platform) + "\x00" + string(profileID) + "\x00" + resumeID + "\x00" + sourceID))
	return ProfileActivitySnapshot{
		ID:       ProfileActivitySnapshotID("activity-snapshot-" + hex.EncodeToString(digest[:16])),
		Platform: platform, ProfileID: profileID, ResumeID: resumeID, SourceID: sourceID,
		ObservedAt: observedAt.UTC(),
	}, nil
}

func (snapshot ProfileActivitySnapshot) Validate() error {
	if snapshot.ID == "" {
		return errors.New("profile activity snapshot requires id")
	}
	expected, err := NewProfileActivitySnapshot(snapshot.Platform, snapshot.ProfileID, snapshot.ResumeID, snapshot.SourceID, snapshot.ObservedAt)
	if err != nil {
		return err
	}
	if expected.ID != snapshot.ID {
		return errors.New("profile activity snapshot id does not match its identity")
	}
	for _, counter := range []*int{
		snapshot.Score, snapshot.PeriodDays, snapshot.SearchShows, snapshot.Views, snapshot.NewViews,
		snapshot.Invitations, snapshot.NewInvitations, snapshot.ResponseStreak, snapshot.ResponsesRequired,
	} {
		if counter != nil && *counter < 0 {
			return errors.New("profile activity snapshot counters cannot be negative")
		}
	}
	return nil
}
