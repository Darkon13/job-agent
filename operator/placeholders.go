package operator

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// applicationModelPlaceholdersFactKey is the reserved key inside resume facts
// that declares placeholder values: {"placeholders": {"name": "Иван"}}.
const applicationModelPlaceholdersFactKey = "placeholders"

// applicationModelCompanyPlaceholder is always provided by the operator from
// the vacancy employer and cannot be declared by the user.
const applicationModelCompanyPlaceholder = "company_name"

var (
	applicationModelPlaceholderNamePattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	applicationModelPlaceholderTokenPattern = regexp.MustCompile(`\{([a-z][a-z0-9_]{0,31})\}`)
)

type applicationPlaceholderValue struct {
	name  string
	value string
}

// anonymizeApplicationTemplateData replaces declared personal values and the
// employer name with placeholders before the context reaches the model. The
// returned map carries the real values for the local substitution step.
func anonymizeApplicationTemplateData(data ApplicationTemplateData) (ApplicationTemplateData, map[string]string, error) {
	var rawFacts any
	if data.Resume != nil && data.Resume.Facts[applicationModelPlaceholdersFactKey] != nil {
		rawFacts = data.Resume.Facts[applicationModelPlaceholdersFactKey]
	}
	declared, err := declaredApplicationPlaceholders(rawFacts)
	if err != nil {
		return ApplicationTemplateData{}, nil, err
	}
	if employer := strings.TrimSpace(data.Vacancy.Employer); employer != "" {
		declared[applicationModelCompanyPlaceholder] = employer
	}
	if len(declared) == 0 {
		return data, map[string]string{}, nil
	}

	replacements := applicationPlaceholderReplacements(declared)

	vacancy, err := anonymizedVacancyContext(data.Vacancy, replacements)
	if err != nil {
		return ApplicationTemplateData{}, nil, err
	}
	result := data
	result.Vacancy = vacancy
	result.Employer = vacancy.Employer
	if data.Resume != nil {
		facts, err := anonymizedFacts(data.Resume.Facts, replacements)
		if err != nil {
			return ApplicationTemplateData{}, nil, err
		}
		resume := *data.Resume
		resume.Facts = facts
		result.Resume = &resume
	}
	return result, declared, nil
}

// applicationPlaceholderReplacements orders declared values longest first so
// overlapping names do not leave partial matches.
func applicationPlaceholderReplacements(declared map[string]string) []applicationPlaceholderValue {
	replacements := make([]applicationPlaceholderValue, 0, len(declared))
	for name, value := range declared {
		replacements = append(replacements, applicationPlaceholderValue{name: name, value: value})
	}
	sort.Slice(replacements, func(i, j int) bool {
		if len(replacements[i].value) != len(replacements[j].value) {
			return len(replacements[i].value) > len(replacements[j].value)
		}
		return replacements[i].name < replacements[j].name
	})
	return replacements
}

func declaredApplicationPlaceholders(raw any) (map[string]string, error) {
	values := map[string]string{}
	if raw == nil {
		return values, nil
	}
	mapping, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("resume facts %q must be an object", applicationModelPlaceholdersFactKey)
	}
	for name, value := range mapping {
		text, ok := value.(string)
		if !ok || !applicationModelPlaceholderNamePattern.MatchString(name) || strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("resume facts placeholder %q must be a non-empty string with a lowercase name", name)
		}
		if name == applicationModelCompanyPlaceholder {
			continue
		}
		values[name] = text
	}
	return values, nil
}

func anonymizedVacancyContext(vacancy ApplicationVacancyContext, replacements []applicationPlaceholderValue) (ApplicationVacancyContext, error) {
	encoded, err := json.Marshal(vacancy)
	if err != nil {
		return ApplicationVacancyContext{}, fmt.Errorf("encode vacancy context: %w", err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return ApplicationVacancyContext{}, fmt.Errorf("decode vacancy context: %w", err)
	}
	anonymized, err := json.Marshal(replaceApplicationPlaceholderValues(decoded, replacements))
	if err != nil {
		return ApplicationVacancyContext{}, fmt.Errorf("encode anonymized vacancy context: %w", err)
	}
	var result ApplicationVacancyContext
	if err := json.Unmarshal(anonymized, &result); err != nil {
		return ApplicationVacancyContext{}, fmt.Errorf("decode anonymized vacancy context: %w", err)
	}
	return result, nil
}

func anonymizedFacts(facts map[string]any, replacements []applicationPlaceholderValue) (map[string]any, error) {
	encoded, err := json.Marshal(facts)
	if err != nil {
		return nil, fmt.Errorf("encode resume facts: %w", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, fmt.Errorf("decode resume facts: %w", err)
	}
	delete(decoded, applicationModelPlaceholdersFactKey)
	replaced, ok := replaceApplicationPlaceholderValues(decoded, replacements).(map[string]any)
	if !ok {
		return nil, errors.New("resume facts have an unexpected shape")
	}
	return replaced, nil
}

func replaceApplicationPlaceholderValues(value any, replacements []applicationPlaceholderValue) any {
	switch item := value.(type) {
	case string:
		for _, replacement := range replacements {
			item = strings.ReplaceAll(item, replacement.value, "{"+replacement.name+"}")
		}
		return item
	case map[string]any:
		result := make(map[string]any, len(item))
		for key, child := range item {
			result[key] = replaceApplicationPlaceholderValues(child, replacements)
		}
		return result
	case []any:
		result := make([]any, len(item))
		for index, child := range item {
			result[index] = replaceApplicationPlaceholderValues(child, replacements)
		}
		return result
	default:
		return value
	}
}

// substituteApplicationPlaceholders replaces every declared placeholder with
// its real value. Undeclared tokens are rejected instead of being sent.
func substituteApplicationPlaceholders(text string, values map[string]string) (string, error) {
	var missing []string
	result := applicationModelPlaceholderTokenPattern.ReplaceAllStringFunc(text, func(token string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(token, "{"), "}")
		value, exists := values[name]
		if !exists {
			missing = append(missing, name)
			return token
		}
		return value
	})
	if len(missing) != 0 {
		sort.Strings(missing)
		return "", fmt.Errorf("model returned an undeclared placeholder %q", missing[0])
	}
	return result, nil
}

// undeclaredApplicationPlaceholders lists tokens in the model text that are
// not part of the declared placeholder set.
func undeclaredApplicationPlaceholders(text string, values map[string]string) []string {
	var undeclared []string
	seen := map[string]struct{}{}
	for _, match := range applicationModelPlaceholderTokenPattern.FindAllStringSubmatch(text, -1) {
		name := match[1]
		if _, declared := values[name]; declared {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		undeclared = append(undeclared, name)
	}
	sort.Strings(undeclared)
	return undeclared
}
