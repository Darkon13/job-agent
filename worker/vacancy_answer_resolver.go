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

var _ AnswerBlockResolver = (*ReviewedVacancyAnswers)(nil)

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

// FindQualificationLevel returns the reviewed qualification block of one
// family/level with the latest appended revision applied. A level without a
// declarative block falls back to the conventional reviewed tag, so verified
// model answers stay discoverable after the first passed attempt.
func (resolver *ReviewedVacancyAnswers) FindQualificationLevel(ctx context.Context, platform core.Platform, familyID, levelID string) (core.AnswerBlock, bool, error) {
	base, found := resolver.registry.FindQualificationLevel(platform, familyID, levelID)
	if found {
		return resolver.Get(ctx, base.Tag)
	}
	return resolver.Get(ctx, core.QualificationReviewedBlockTag(platform, familyID, levelID))
}

// Get returns a reviewed block by tag, merging the latest appended revision.
func (resolver *ReviewedVacancyAnswers) Get(ctx context.Context, tag string) (core.AnswerBlock, bool, error) {
	base, found := resolver.registry.Get(tag)
	latest, exists, err := resolver.revisions.LatestAnswerBlockRevision(ctx, tag)
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
