package operator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"text/template"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Darkon13/job-agent/core"
)

const maximumApplicationMessageRunes = 10000

type ApplicationOutcome string

const (
	ApplicationApply  ApplicationOutcome = "apply"
	ApplicationSkip   ApplicationOutcome = "skip"
	ApplicationReview ApplicationOutcome = "review"
)

type ApplicationPreparation struct {
	Outcome ApplicationOutcome
	Code    string
	Reason  string
	Message string
}

func (preparation ApplicationPreparation) Validate() error {
	switch preparation.Outcome {
	case ApplicationApply, ApplicationSkip, ApplicationReview:
	default:
		return fmt.Errorf("unknown application outcome %q", preparation.Outcome)
	}
	if strings.TrimSpace(preparation.Code) == "" || strings.TrimSpace(preparation.Reason) == "" {
		return errors.New("application preparation requires code and reason")
	}
	if !utf8.ValidString(preparation.Message) || utf8.RuneCountInString(preparation.Message) > maximumApplicationMessageRunes {
		return errors.New("application message must be valid UTF-8 and at most 10000 characters")
	}
	if preparation.Outcome != ApplicationApply && strings.TrimSpace(preparation.Message) != "" {
		return errors.New("skipped or reviewed application must not contain a message")
	}
	return nil
}

type ApplicationPreparer interface {
	PrepareApplication(ctx context.Context, application core.Application, vacancy core.Vacancy) (ApplicationPreparation, error)
}

type RuleTemplateConfig struct {
	IncludeAny      []string
	ExcludeAny      []string
	StaticMessage   string
	MessageTemplate string
}

type RuleTemplatePreparer struct {
	includeAny    []string
	excludeAny    []string
	staticMessage string
	template      *template.Template
}

type ApplicationTemplateData struct {
	ApplicationID string                    `json:"application_id"`
	ProfileID     string                    `json:"profile_id"`
	Vacancy       ApplicationVacancyContext `json:"vacancy"`

	// Flat fields are retained for existing templates. New templates should use
	// Vacancy so the same structured context can later be passed to a model
	// operator without inventing a second schema.
	Title       string   `json:"-"`
	Employer    string   `json:"-"`
	URL         string   `json:"-"`
	Description string   `json:"-"`
	KeySkills   []string `json:"-"`
}

type ApplicationVacancyContext struct {
	Platform    string         `json:"platform"`
	ExternalID  string         `json:"external_id"`
	URL         string         `json:"url"`
	Title       string         `json:"title"`
	Employer    string         `json:"employer,omitempty"`
	State       string         `json:"state"`
	PublishedAt *time.Time     `json:"published_at,omitempty"`
	Description string         `json:"description,omitempty"`
	KeySkills   []string       `json:"key_skills,omitempty"`
	Attributes  map[string]any `json:"attributes,omitempty"`
}

func NewRuleTemplatePreparer(config RuleTemplateConfig) (*RuleTemplatePreparer, error) {
	includeAny, err := normalizeTerms("include_any", config.IncludeAny)
	if err != nil {
		return nil, err
	}
	excludeAny, err := normalizeTerms("exclude_any", config.ExcludeAny)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.StaticMessage) != "" && strings.TrimSpace(config.MessageTemplate) != "" {
		return nil, errors.New("application operator accepts either static message or message template")
	}
	var compiled *template.Template
	if strings.TrimSpace(config.MessageTemplate) != "" {
		compiled, err = template.New("cover-letter").Option("missingkey=error").Parse(config.MessageTemplate)
		if err != nil {
			return nil, fmt.Errorf("parse cover letter template: %w", err)
		}
		var probe bytes.Buffer
		if err := compiled.Execute(&probe, ApplicationTemplateData{Vacancy: ApplicationVacancyContext{Attributes: map[string]any{}}}); err != nil {
			return nil, fmt.Errorf("validate cover letter template: %w", err)
		}
		if !utf8.ValidString(probe.String()) || utf8.RuneCountInString(probe.String()) > maximumApplicationMessageRunes {
			return nil, errors.New("cover letter template output must be valid UTF-8 and at most 10000 characters")
		}
	}
	preparer := &RuleTemplatePreparer{
		includeAny: includeAny, excludeAny: excludeAny,
		staticMessage: strings.TrimSpace(config.StaticMessage), template: compiled,
	}
	probe := ApplicationPreparation{Outcome: ApplicationApply, Code: "qualified", Reason: "vacancy passed deterministic rules", Message: preparer.staticMessage}
	if err := probe.Validate(); err != nil {
		return nil, err
	}
	return preparer, nil
}

func (preparer *RuleTemplatePreparer) PrepareApplication(ctx context.Context, application core.Application, vacancy core.Vacancy) (ApplicationPreparation, error) {
	if err := ctx.Err(); err != nil {
		return ApplicationPreparation{}, err
	}
	if preparer == nil {
		return ApplicationPreparation{}, errors.New("application preparer is nil")
	}
	if err := vacancy.Validate(); err != nil {
		return ApplicationPreparation{}, err
	}
	searchable := vacancySearchableText(vacancy)
	for _, term := range preparer.excludeAny {
		if containsTerm(searchable, term) {
			return ApplicationPreparation{
				Outcome: ApplicationSkip, Code: "excluded_term",
				Reason: fmt.Sprintf("vacancy contains excluded term %q", term),
			}, nil
		}
	}
	if len(preparer.includeAny) != 0 {
		matched := false
		for _, term := range preparer.includeAny {
			if containsTerm(searchable, term) {
				matched = true
				break
			}
		}
		if !matched {
			return ApplicationPreparation{
				Outcome: ApplicationSkip, Code: "include_term_missing",
				Reason: "vacancy contains none of the required terms",
			}, nil
		}
	}
	message, err := preparer.renderMessage(application, vacancy)
	if err != nil {
		return ApplicationPreparation{}, err
	}
	result := ApplicationPreparation{
		Outcome: ApplicationApply, Code: "qualified",
		Reason: "vacancy passed deterministic rules", Message: message,
	}
	if err := result.Validate(); err != nil {
		return ApplicationPreparation{}, err
	}
	return result, nil
}

func (preparer *RuleTemplatePreparer) renderMessage(application core.Application, vacancy core.Vacancy) (string, error) {
	if preparer.template == nil {
		return preparer.staticMessage, nil
	}
	data := NewApplicationTemplateData(application, vacancy)
	var output bytes.Buffer
	if err := preparer.template.Execute(&output, data); err != nil {
		return "", fmt.Errorf("render cover letter template: %w", err)
	}
	return strings.TrimSpace(output.String()), nil
}

func NewApplicationTemplateData(application core.Application, vacancy core.Vacancy) ApplicationTemplateData {
	description := vacancyAttributeString(vacancy, "description")
	keySkills := vacancyAttributeStrings(vacancy, "key_skills")
	var publishedAt *time.Time
	if vacancy.PublishedAt != nil {
		value := *vacancy.PublishedAt
		publishedAt = &value
	}
	context := ApplicationVacancyContext{
		Platform: string(vacancy.Platform), ExternalID: vacancy.ExternalID, URL: vacancy.URL,
		Title: vacancy.Title, Employer: vacancy.Employer, State: string(vacancy.State), PublishedAt: publishedAt,
		Description: description, KeySkills: append([]string(nil), keySkills...), Attributes: cloneVacancyAttributes(vacancy.Attributes),
	}
	return ApplicationTemplateData{
		ApplicationID: string(application.ID), ProfileID: string(application.Key.ProfileID), Vacancy: context,
		Title: context.Title, Employer: context.Employer, URL: context.URL,
		Description: context.Description, KeySkills: append([]string(nil), context.KeySkills...),
	}
}

func cloneVacancyAttributes(attributes map[string]any) map[string]any {
	if attributes == nil {
		return nil
	}
	result := make(map[string]any, len(attributes))
	for key, value := range attributes {
		result[key] = cloneVacancyAttributeValue(value)
	}
	return result
}

func cloneVacancyAttributeValue(value any) any {
	switch item := value.(type) {
	case map[string]any:
		return cloneVacancyAttributes(item)
	case []any:
		result := make([]any, len(item))
		for index, child := range item {
			result[index] = cloneVacancyAttributeValue(child)
		}
		return result
	case []string:
		return append([]string(nil), item...)
	default:
		return item
	}
}

func normalizeTerms(field string, values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		term := strings.ToLower(strings.TrimSpace(value))
		if term == "" {
			return nil, fmt.Errorf("%s contains an empty term", field)
		}
		if _, exists := seen[term]; exists {
			return nil, fmt.Errorf("%s contains duplicate term %q", field, value)
		}
		seen[term] = struct{}{}
		result = append(result, term)
	}
	return result, nil
}

func vacancySearchableText(vacancy core.Vacancy) string {
	parts := []string{vacancy.Title, vacancy.Employer, stripHTML(vacancyAttributeString(vacancy, "description"))}
	parts = append(parts, vacancyAttributeStrings(vacancy, "key_skills")...)
	return strings.ToLower(strings.Join(parts, "\n"))
}

func containsTerm(text, term string) bool {
	for offset := 0; offset <= len(text)-len(term); {
		index := strings.Index(text[offset:], term)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(term)
		first, _ := utf8.DecodeRuneInString(term)
		last, _ := utf8.DecodeLastRuneInString(term)
		beforeOK := start == 0 || !isWordRune(previousRune(text[:start])) || !isWordRune(first)
		afterOK := end == len(text) || !isWordRune(nextRune(text[end:])) || !isWordRune(last)
		if beforeOK && afterOK {
			return true
		}
		_, size := utf8.DecodeRuneInString(text[start:])
		offset = start + size
	}
	return false
}

func previousRune(value string) rune {
	symbol, _ := utf8.DecodeLastRuneInString(value)
	return symbol
}

func nextRune(value string) rune {
	symbol, _ := utf8.DecodeRuneInString(value)
	return symbol
}

func isWordRune(symbol rune) bool {
	return unicode.IsLetter(symbol) || unicode.IsNumber(symbol) || symbol == '_'
}

func stripHTML(value string) string {
	var output strings.Builder
	insideTag := false
	for _, symbol := range value {
		switch symbol {
		case '<':
			insideTag = true
		case '>':
			insideTag = false
			output.WriteByte(' ')
		default:
			if !insideTag {
				output.WriteRune(symbol)
			}
		}
	}
	return html.UnescapeString(output.String())
}

func vacancyAttributeString(vacancy core.Vacancy, key string) string {
	value, _ := vacancy.Attributes[key].(string)
	return value
}

func vacancyAttributeStrings(vacancy core.Vacancy, key string) []string {
	switch values := vacancy.Attributes[key].(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		result := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}
