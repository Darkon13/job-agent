package adapter

import (
	"context"
	"time"

	"github.com/Darkon13/job-agent/core"
)

// QualificationCatalogReader exposes the skill verification catalog available
// to a profile. Discovery never starts an attempt.
type QualificationCatalogReader interface {
	SyncQualifications(ctx context.Context, profileID core.ProfileID) ([]core.QualificationOffering, error)
}

// QualificationSession is the runtime handle of a started attempt. It stays
// inside the adapter: only the platform transport knows how to continue it.
type QualificationSession struct {
	ProfileID  core.ProfileID
	OfferingID core.QualificationID
	AttemptID  string
	StartedAt  time.Time
}

// QualificationAttemptService runs an explicitly started attempt. Starting may
// consume a limited or timed attempt, so it is not part of discovery.
type QualificationAttemptService interface {
	StartQualification(ctx context.Context, profileID core.ProfileID, offeringID core.QualificationID) (QualificationSession, error)
	CurrentQuestion(ctx context.Context, session QualificationSession) (core.Question, error)
	SubmitAnswer(ctx context.Context, session QualificationSession, answer core.ResolvedAnswer) error
	AttemptResult(ctx context.Context, session QualificationSession) (core.QualificationResult, error)
}
