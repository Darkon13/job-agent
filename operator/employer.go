package operator

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/Darkon13/job-agent/core"
)

type EmployerGroupRuleConfig struct {
	Platform   core.Platform
	EmployerID string
	Name       string
}

type EmployerGroupConfig struct {
	Tag     string
	Rules   []EmployerGroupRuleConfig
	Include []string
}

type EmployerGroupEvidence struct {
	GroupTag string        `json:"group_tag"`
	Kind     string        `json:"kind"`
	Platform core.Platform `json:"platform,omitempty"`
	Value    string        `json:"value,omitempty"`
	Via      string        `json:"via,omitempty"`
}

type EmployerGroupMatch struct {
	GroupTag string                `json:"group_tag"`
	Evidence EmployerGroupEvidence `json:"evidence"`
}

type compiledEmployerGroupRule struct {
	platform   core.Platform
	employerID string
	name       string
}

type compiledEmployerGroup struct {
	tag     string
	rules   []compiledEmployerGroupRule
	include []string
}

// EmployerGroupMatcher evaluates explicit identities only. It deliberately
// does not use substring or fuzzy matching: a similar company name is not
// proof that two employers belong to one group.
type EmployerGroupMatcher struct {
	order  []string
	groups map[string]compiledEmployerGroup
}

func NewEmployerGroupMatcher(configs []EmployerGroupConfig) (*EmployerGroupMatcher, error) {
	matcher := &EmployerGroupMatcher{order: make([]string, 0, len(configs)), groups: make(map[string]compiledEmployerGroup, len(configs))}
	for _, config := range configs {
		tag := strings.TrimSpace(config.Tag)
		if tag == "" {
			return nil, errors.New("employer group requires tag")
		}
		if _, exists := matcher.groups[tag]; exists {
			return nil, fmt.Errorf("duplicate employer group tag %q", tag)
		}
		group := compiledEmployerGroup{tag: tag, include: make([]string, 0, len(config.Include))}
		seenRules := make(map[string]struct{}, len(config.Rules))
		for _, configured := range config.Rules {
			rule, key, err := compileEmployerGroupRule(tag, configured)
			if err != nil {
				return nil, err
			}
			if _, exists := seenRules[key]; exists {
				return nil, fmt.Errorf("employer group %q contains duplicate identity %q", tag, key)
			}
			seenRules[key] = struct{}{}
			group.rules = append(group.rules, rule)
		}
		seenIncludes := make(map[string]struct{}, len(config.Include))
		for _, value := range config.Include {
			included := strings.TrimSpace(value)
			if included == "" {
				return nil, fmt.Errorf("employer group %q contains an empty include", tag)
			}
			if _, exists := seenIncludes[included]; exists {
				return nil, fmt.Errorf("employer group %q contains duplicate include %q", tag, included)
			}
			seenIncludes[included] = struct{}{}
			group.include = append(group.include, included)
		}
		if len(group.rules) == 0 && len(group.include) == 0 {
			return nil, fmt.Errorf("employer group %q requires an identity or included group", tag)
		}
		matcher.order = append(matcher.order, tag)
		matcher.groups[tag] = group
	}
	for _, tag := range matcher.order {
		for _, included := range matcher.groups[tag].include {
			if _, exists := matcher.groups[included]; !exists {
				return nil, fmt.Errorf("employer group %q includes unknown group %q", tag, included)
			}
		}
	}
	if err := matcher.validateAcyclic(); err != nil {
		return nil, err
	}
	return matcher, nil
}

func compileEmployerGroupRule(groupTag string, configured EmployerGroupRuleConfig) (compiledEmployerGroupRule, string, error) {
	platform := core.Platform(strings.TrimSpace(string(configured.Platform)))
	employerID := strings.TrimSpace(configured.EmployerID)
	name := normalizeEmployerName(configured.Name)
	if (employerID == "") == (name == "") {
		return compiledEmployerGroupRule{}, "", fmt.Errorf("employer group %q identity requires exactly one of employer_id or name", groupTag)
	}
	if employerID != "" && platform == "" {
		return compiledEmployerGroupRule{}, "", fmt.Errorf("employer group %q employer_id %q requires platform", groupTag, employerID)
	}
	rule := compiledEmployerGroupRule{platform: platform, employerID: employerID, name: name}
	return rule, string(platform) + "\x00" + employerID + "\x00" + name, nil
}

func (matcher *EmployerGroupMatcher) Match(vacancy core.Vacancy) []EmployerGroupMatch {
	if matcher == nil {
		return nil
	}
	matches := make([]EmployerGroupMatch, 0)
	cache := make(map[string]*EmployerGroupEvidence, len(matcher.groups))
	for _, tag := range matcher.order {
		if evidence, matched := matcher.matchGroup(tag, vacancy, cache); matched {
			matches = append(matches, EmployerGroupMatch{GroupTag: tag, Evidence: evidence})
		}
	}
	return matches
}

func (matcher *EmployerGroupMatcher) MatchAny(vacancy core.Vacancy, tags []string) (EmployerGroupMatch, bool) {
	if matcher == nil {
		return EmployerGroupMatch{}, false
	}
	cache := make(map[string]*EmployerGroupEvidence, len(matcher.groups))
	for _, value := range tags {
		tag := strings.TrimSpace(value)
		if evidence, matched := matcher.matchGroup(tag, vacancy, cache); matched {
			return EmployerGroupMatch{GroupTag: tag, Evidence: evidence}, true
		}
	}
	return EmployerGroupMatch{}, false
}

func (matcher *EmployerGroupMatcher) HasGroup(tag string) bool {
	if matcher == nil {
		return false
	}
	_, exists := matcher.groups[strings.TrimSpace(tag)]
	return exists
}

func (matcher *EmployerGroupMatcher) matchGroup(tag string, vacancy core.Vacancy, cache map[string]*EmployerGroupEvidence) (EmployerGroupEvidence, bool) {
	if cached, exists := cache[tag]; exists {
		if cached == nil {
			return EmployerGroupEvidence{}, false
		}
		return *cached, true
	}
	group, exists := matcher.groups[tag]
	if !exists {
		cache[tag] = nil
		return EmployerGroupEvidence{}, false
	}
	employerID := vacancyEmployerID(vacancy)
	for _, rule := range group.rules {
		if rule.employerID != "" && vacancy.Platform == rule.platform && employerID == rule.employerID {
			evidence := EmployerGroupEvidence{GroupTag: tag, Kind: "employer_id", Platform: vacancy.Platform, Value: employerID}
			cache[tag] = &evidence
			return evidence, true
		}
	}
	name := normalizeEmployerName(vacancy.Employer)
	for _, rule := range group.rules {
		if rule.name != "" && (rule.platform == "" || vacancy.Platform == rule.platform) && name == rule.name {
			evidence := EmployerGroupEvidence{GroupTag: tag, Kind: "name", Platform: vacancy.Platform, Value: vacancy.Employer}
			cache[tag] = &evidence
			return evidence, true
		}
	}
	for _, included := range group.include {
		if child, matched := matcher.matchGroup(included, vacancy, cache); matched {
			evidence := EmployerGroupEvidence{GroupTag: tag, Kind: "included_group", Platform: vacancy.Platform, Via: child.GroupTag}
			cache[tag] = &evidence
			return evidence, true
		}
	}
	cache[tag] = nil
	return EmployerGroupEvidence{}, false
}

func (matcher *EmployerGroupMatcher) validateAcyclic() error {
	const (
		unseen = iota
		visiting
		done
	)
	state := make(map[string]int, len(matcher.groups))
	stack := make([]string, 0, len(matcher.groups))
	var visit func(string) error
	visit = func(tag string) error {
		switch state[tag] {
		case visiting:
			start := 0
			for index, value := range stack {
				if value == tag {
					start = index
					break
				}
			}
			cycle := append(append([]string(nil), stack[start:]...), tag)
			return fmt.Errorf("employer group include cycle: %s", strings.Join(cycle, " -> "))
		case done:
			return nil
		}
		state[tag] = visiting
		stack = append(stack, tag)
		for _, included := range matcher.groups[tag].include {
			if err := visit(included); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[tag] = done
		return nil
	}
	for _, tag := range matcher.order {
		if err := visit(tag); err != nil {
			return err
		}
	}
	return nil
}

func normalizeEmployerName(value string) string {
	return strings.Map(func(symbol rune) rune {
		if unicode.IsSpace(symbol) {
			return ' '
		}
		return unicode.ToLower(symbol)
	}, strings.Join(strings.Fields(value), " "))
}

func vacancyEmployerID(vacancy core.Vacancy) string {
	value, exists := vacancy.Attributes["employer_id"]
	if !exists {
		return ""
	}
	switch item := value.(type) {
	case string:
		return strings.TrimSpace(item)
	case fmt.Stringer:
		return strings.TrimSpace(item.String())
	case float64:
		return strconv.FormatFloat(item, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(item), 'f', -1, 32)
	case int:
		return strconv.Itoa(item)
	case int64:
		return strconv.FormatInt(item, 10)
	case int32:
		return strconv.FormatInt(int64(item), 10)
	case uint:
		return strconv.FormatUint(uint64(item), 10)
	case uint64:
		return strconv.FormatUint(item, 10)
	case uint32:
		return strconv.FormatUint(uint64(item), 10)
	default:
		return ""
	}
}
