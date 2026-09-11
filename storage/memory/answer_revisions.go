package memory

import (
	"context"
	"errors"
	"strings"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

var _ storage.AnswerBlockRevisionRepository = (*Repository)(nil)

func (repository *Repository) AppendAnswerBlockRevision(ctx context.Context, candidate core.AnswerBlockRevision) (core.AnswerBlockRevision, error) {
	if err := ctx.Err(); err != nil {
		return core.AnswerBlockRevision{}, err
	}
	if strings.TrimSpace(candidate.BlockTag) == "" || strings.TrimSpace(candidate.Source) == "" || candidate.CreatedAt.IsZero() {
		return core.AnswerBlockRevision{}, errors.New("answer block revision requires tag, source and created_at")
	}
	if err := core.ValidateAnswerBlock(candidate.Block()); err != nil {
		return core.AnswerBlockRevision{}, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	revisions := repository.answerRevisions[candidate.BlockTag]
	candidate.Revision = uint64(len(revisions)) + 1
	stored := cloneAnswerBlockRevision(candidate)
	repository.answerRevisions[candidate.BlockTag] = append(revisions, stored)
	return cloneAnswerBlockRevision(stored), nil
}

func (repository *Repository) LatestAnswerBlockRevision(ctx context.Context, tag string) (core.AnswerBlockRevision, bool, error) {
	if err := ctx.Err(); err != nil {
		return core.AnswerBlockRevision{}, false, err
	}
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return core.AnswerBlockRevision{}, false, errors.New("answer block revision tag is required")
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	revisions := repository.answerRevisions[tag]
	if len(revisions) == 0 {
		return core.AnswerBlockRevision{}, false, nil
	}
	return cloneAnswerBlockRevision(revisions[len(revisions)-1]), true, nil
}

func cloneAnswerBlockRevision(source core.AnswerBlockRevision) core.AnswerBlockRevision {
	result := source
	result.Answers = make([]core.StoredAnswer, len(source.Answers))
	for index, answer := range source.Answers {
		result.Answers[index] = answer
		result.Answers[index].SelectedOptions = append([]string(nil), answer.SelectedOptions...)
	}
	return result
}
