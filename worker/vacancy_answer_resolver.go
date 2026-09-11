package worker

import (
	"context"
	"errors"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

// ReviewedVacancyAnswers merges declarative config blocks with the latest
// append-only revision written by human review. The latest human knowledge
// wins for an already answered question.
type ReviewedVacancyAnswers struct {
	registry  *core.AnswerBlockRegistry
	revisions storage.AnswerBlockRevisionRepository
}

func NewReviewedVacancyAnswers(registry *core.AnswerBlockRegistry, revisions storage.AnswerBlockRevisionRepository) (*ReviewedVacancyAnswers, error) {
	if registry == nil || revisions == nil {
		return nil, errors.New("reviewed vacancy answers require registry and revisions repository")
	}
	return &ReviewedVacancyAnswers{registry: registry, revisions: revisions}, nil
}

func (resolver *ReviewedVacancyAnswers) FindVacancy(ctx context.Context, platform core.Platform) (core.AnswerBlock, bool, error) {
	base, found := resolver.registry.FindVacancy(platform)
	latest, exists, err := resolver.revisions.LatestAnswerBlockRevision(ctx, VacancyReviewedBlockTag(platform, base, found))
	if err != nil {
		return core.AnswerBlock{}, false, err
	}
	if !exists {
		return base, found, nil
	}
	stored := latest.Block()
	if !found {
		return stored, true, nil
	}
	merged, err := core.MergeAnswerBlocks(base, stored)
	if err != nil {
		return core.AnswerBlock{}, false, err
	}
	return merged, true, nil
}

// VacancyReviewedBlockTag returns the tag human review extends: the configured
// vacancy block when one exists, otherwise a per-platform reviewed block.
func VacancyReviewedBlockTag(platform core.Platform, base core.AnswerBlock, found bool) string {
	if found {
		return base.Tag
	}
	return string(platform) + "-vacancy-reviewed"
}
