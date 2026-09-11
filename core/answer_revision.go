package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// AnswerBlockRevision is one append-only snapshot of a reviewed answer block.
// Human review extends the bank without editing the declarative config; the
// latest revision is the most recent reviewed knowledge.
type AnswerBlockRevision struct {
	BlockTag  string          `json:"block_tag"`
	Name      string          `json:"name"`
	Kind      AnswerBlockKind `json:"kind"`
	Platform  Platform        `json:"platform"`
	Revision  uint64          `json:"revision"`
	Source    string          `json:"source"`
	Answers   []StoredAnswer  `json:"answers"`
	CreatedAt time.Time       `json:"created_at"`
}

func (revision AnswerBlockRevision) Validate() error {
	if strings.TrimSpace(revision.BlockTag) == "" || revision.Revision == 0 || revision.CreatedAt.IsZero() {
		return errors.New("answer block revision requires tag, revision and created_at")
	}
	if strings.TrimSpace(revision.Source) == "" {
		return errors.New("answer block revision requires source")
	}
	if err := ValidateAnswerBlock(revision.Block()); err != nil {
		return err
	}
	return nil
}

func (revision AnswerBlockRevision) Block() AnswerBlock {
	return AnswerBlock{
		Tag: revision.BlockTag, Name: revision.Name, Kind: revision.Kind, Platform: revision.Platform,
		Answers: append([]StoredAnswer(nil), revision.Answers...),
	}
}

// StoredAnswersDigest is an order-independent digest of the answers. It is used
// to skip appending a revision whose content is unchanged.
func StoredAnswersDigest(answers []StoredAnswer) (string, error) {
	type canonical struct {
		Question    string   `json:"question"`
		Fingerprint string   `json:"question_fingerprint,omitempty"`
		Selected    []string `json:"selected_options,omitempty"`
		Text        string   `json:"text,omitempty"`
	}
	items := make([]canonical, 0, len(answers))
	for _, answer := range answers {
		selected := append([]string(nil), answer.SelectedOptions...)
		sort.Strings(selected)
		items = append(items, canonical{
			Question: NormalizeQuestionText(answer.Question), Fingerprint: answer.QuestionFingerprint,
			Selected: selected, Text: answer.Text,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Question < items[j].Question })
	encoded, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("encode stored answers: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// MergeAnswerBlocks returns base with latest answers applied on top. A latest
// answer for an already known question replaces the base answer because an
// appended revision is the most recent reviewed knowledge. Both blocks must
// belong to the same platform and kind.
func MergeAnswerBlocks(base, latest AnswerBlock) (AnswerBlock, error) {
	if err := ValidateAnswerBlock(base); err != nil {
		return AnswerBlock{}, err
	}
	if err := ValidateAnswerBlock(latest); err != nil {
		return AnswerBlock{}, err
	}
	if base.Kind != latest.Kind || base.Platform != latest.Platform {
		return AnswerBlock{}, errors.New("answer blocks belong to different kinds or platforms")
	}
	merged := cloneAnswerBlock(base)
	index := make(map[string]int, len(merged.Answers))
	for position, answer := range merged.Answers {
		index[NormalizeQuestionText(answer.Question)] = position
	}
	for _, answer := range latest.Answers {
		key := NormalizeQuestionText(answer.Question)
		if position, exists := index[key]; exists {
			merged.Answers[position] = cloneStoredAnswer(answer)
			continue
		}
		merged.Answers = append(merged.Answers, cloneStoredAnswer(answer))
		index[key] = len(merged.Answers) - 1
	}
	return merged, nil
}

func cloneStoredAnswer(answer StoredAnswer) StoredAnswer {
	answer.SelectedOptions = append([]string(nil), answer.SelectedOptions...)
	return answer
}
