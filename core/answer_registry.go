package core

import (
	"fmt"
)

type AnswerBlockRegistry struct {
	byTag                   map[string]AnswerBlock
	qualificationLevel      map[string]string
	conversationTopic       map[string]string
	conversationFingerprint map[string]string
	vacancy                 map[Platform]string
}

func NewAnswerBlockRegistry(blocks ...AnswerBlock) (*AnswerBlockRegistry, error) {
	registry := &AnswerBlockRegistry{
		byTag:              make(map[string]AnswerBlock, len(blocks)),
		qualificationLevel: make(map[string]string),
		conversationTopic:  make(map[string]string), conversationFingerprint: make(map[string]string),
		vacancy: make(map[Platform]string),
	}
	for _, block := range blocks {
		if err := ValidateAnswerBlock(block); err != nil {
			return nil, fmt.Errorf("answer block %q: %w", block.Tag, err)
		}
		if _, exists := registry.byTag[block.Tag]; exists {
			return nil, fmt.Errorf("duplicate answer block tag %q", block.Tag)
		}
		block = cloneAnswerBlock(block)
		registry.byTag[block.Tag] = block
		if block.Kind == AnswerBlockQualification && block.Qualification != nil {
			key := qualificationLevelKey(block.Platform, block.Qualification.FamilyID, block.Qualification.LevelID)
			if existing, exists := registry.qualificationLevel[key]; exists {
				return nil, fmt.Errorf("qualification blocks %q and %q have the same family and level", existing, block.Tag)
			}
			registry.qualificationLevel[key] = block.Tag
		}
		if block.Kind == AnswerBlockVacancy {
			if existing, exists := registry.vacancy[block.Platform]; exists {
				return nil, fmt.Errorf("vacancy blocks %q and %q target the same platform", existing, block.Tag)
			}
			registry.vacancy[block.Platform] = block.Tag
		}
		if block.Kind == AnswerBlockConversation && block.Match.Topic != "" {
			key := answerMatcherKey(block.Platform, NormalizeQuestionText(block.Match.Topic))
			if existing, exists := registry.conversationTopic[key]; exists {
				return nil, fmt.Errorf("conversation blocks %q and %q have the same topic matcher", existing, block.Tag)
			}
			registry.conversationTopic[key] = block.Tag
		}
		if block.Kind == AnswerBlockConversation && block.Match.Fingerprint != "" {
			key := answerMatcherKey(block.Platform, block.Match.Fingerprint)
			if existing, exists := registry.conversationFingerprint[key]; exists {
				return nil, fmt.Errorf("conversation blocks %q and %q have the same fingerprint matcher", existing, block.Tag)
			}
			registry.conversationFingerprint[key] = block.Tag
		}
	}
	return registry, nil
}

// FindQualificationLevel returns the reviewed question bank for one platform
// qualification family/level before future questions in an attempt are known.
func (registry *AnswerBlockRegistry) FindQualificationLevel(platform Platform, familyID, levelID string) (AnswerBlock, bool) {
	if registry == nil || platform == "" || familyID == "" || levelID == "" {
		return AnswerBlock{}, false
	}
	tag, exists := registry.qualificationLevel[qualificationLevelKey(platform, familyID, levelID)]
	if !exists {
		return AnswerBlock{}, false
	}
	return registry.Get(tag)
}

func (registry *AnswerBlockRegistry) Get(tag string) (AnswerBlock, bool) {
	if registry == nil {
		return AnswerBlock{}, false
	}
	block, exists := registry.byTag[tag]
	return cloneAnswerBlock(block), exists
}

// FindVacancy returns the reviewed question bank of vacancy popup tests for one
// platform. At most one vacancy block per platform is allowed.
func (registry *AnswerBlockRegistry) FindVacancy(platform Platform) (AnswerBlock, bool) {
	if registry == nil || platform == "" {
		return AnswerBlock{}, false
	}
	tag, exists := registry.vacancy[platform]
	if !exists {
		return AnswerBlock{}, false
	}
	return registry.Get(tag)
}

func (registry *AnswerBlockRegistry) FindConversationTopic(platform Platform, topic string) (AnswerBlock, bool) {
	if registry == nil || platform == "" || NormalizeQuestionText(topic) == "" {
		return AnswerBlock{}, false
	}
	tag, exists := registry.conversationTopic[answerMatcherKey(platform, NormalizeQuestionText(topic))]
	if !exists {
		return AnswerBlock{}, false
	}
	return registry.Get(tag)
}

func (registry *AnswerBlockRegistry) FindConversationFingerprint(platform Platform, fingerprint string) (AnswerBlock, bool) {
	if registry == nil || platform == "" || fingerprint == "" {
		return AnswerBlock{}, false
	}
	tag, exists := registry.conversationFingerprint[answerMatcherKey(platform, fingerprint)]
	if !exists {
		return AnswerBlock{}, false
	}
	return registry.Get(tag)
}

func answerMatcherKey(platform Platform, matcher string) string {
	return string(platform) + "\x00" + matcher
}

func qualificationLevelKey(platform Platform, familyID, levelID string) string {
	return string(platform) + "\x00" + familyID + "\x00" + levelID
}

func cloneAnswerBlock(block AnswerBlock) AnswerBlock {
	if block.Qualification != nil {
		qualification := cloneQualificationDescriptor(*block.Qualification)
		block.Qualification = &qualification
	}
	block.Answers = append([]StoredAnswer(nil), block.Answers...)
	for index := range block.Answers {
		block.Answers[index].SelectedOptions = append([]string(nil), block.Answers[index].SelectedOptions...)
	}
	return block
}
