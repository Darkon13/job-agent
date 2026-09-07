package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func recordProfileActivity(ctx context.Context, repository storage.ProfileActivityRepository, platform core.Platform, profileID core.ProfileID, resumeID string, kind core.ProfileActivityKind, sourceID string, occurredAt time.Time) error {
	record, err := core.NewProfileActivityRecord(platform, profileID, resumeID, kind, sourceID, occurredAt)
	if err != nil {
		return err
	}
	if _, err := repository.RecordProfileActivity(ctx, record); err != nil {
		return fmt.Errorf("record profile activity %s: %w", kind, err)
	}
	return nil
}
