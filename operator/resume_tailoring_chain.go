package operator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Darkon13/job-agent/core"
)

type ChainResumeTailoringConfig struct {
	Tag        string
	Version    string
	Processors []ResumeTailoringProcessor
}

// ChainResumeTailoringProcessor merges the overrides of several tailoring
// processors into one plan. Duplicate target paths are rejected instead of
// silently overwriting each other.
type ChainResumeTailoringProcessor struct {
	tag        string
	version    string
	processors []ResumeTailoringProcessor
}

func NewChainResumeTailoringProcessor(config ChainResumeTailoringConfig) (*ChainResumeTailoringProcessor, error) {
	processor := &ChainResumeTailoringProcessor{
		tag: strings.TrimSpace(config.Tag), version: strings.TrimSpace(config.Version),
		processors: append([]ResumeTailoringProcessor(nil), config.Processors...),
	}
	if processor.tag == "" || processor.version == "" {
		return nil, errors.New("chain resume tailoring requires tag and version")
	}
	if len(processor.processors) < 2 {
		return nil, errors.New("chain resume tailoring requires at least two processors")
	}
	for _, child := range processor.processors {
		if child == nil {
			return nil, errors.New("chain resume tailoring requires non-nil processors")
		}
	}
	return processor, nil
}

func (processor *ChainResumeTailoringProcessor) Plan(ctx context.Context, input ResumeTailoringInput) (ResumeTailoringPlan, error) {
	if processor == nil {
		return ResumeTailoringPlan{}, errors.New("chain resume tailoring processor is nil")
	}
	if err := input.Validate(); err != nil {
		return ResumeTailoringPlan{}, err
	}
	overrides := make([]core.ProfileStateValueOverride, 0)
	seen := make(map[string]struct{})
	digests := make([]string, 0, len(processor.processors))
	for _, child := range processor.processors {
		plan, err := child.Plan(ctx, input)
		if err != nil {
			return ResumeTailoringPlan{}, fmt.Errorf("chain resume tailoring child failed: %w", err)
		}
		digests = append(digests, plan.InputDigest)
		for _, override := range plan.Overrides {
			if _, exists := seen[override.Path]; exists {
				return ResumeTailoringPlan{}, fmt.Errorf("chain resume tailoring produced duplicate path %q", override.Path)
			}
			seen[override.Path] = struct{}{}
			overrides = append(overrides, override)
		}
	}
	plan := ResumeTailoringPlan{
		ProcessorTag: processor.tag, ProcessorVersion: processor.version,
		InputDigest: applicationTextDigest(strings.Join(digests, "\n")), Overrides: overrides,
	}
	if err := plan.Validate(input); err != nil {
		return ResumeTailoringPlan{}, err
	}
	return plan, nil
}
