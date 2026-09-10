package operator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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
	Outcome    ApplicationOutcome
	Code       string
	Reason     string
	Message    string
	Provenance core.ApplicationPreparationProvenance
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
	if preparation.Outcome != ApplicationApply && !preparation.Provenance.IsZero() {
		return errors.New("skipped or reviewed application must not contain preparation provenance")
	}
	if err := preparation.Provenance.Validate(); err != nil {
		return err
	}
	if !preparation.Provenance.IsZero() && preparation.Provenance.OutputDigest != applicationTextDigest(strings.TrimSpace(preparation.Message)) {
		return errors.New("application preparation provenance output digest does not match message")
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
	MessagePool     *MessagePoolConfig
	Model           *ApplicationModelConfig
	Resume          *ApplicationResumeContext
	EmployerMatcher *EmployerGroupMatcher
	EmployerRules   []EmployerRuleConfig
}

const (
	MessagePoolFirst      = "first"
	MessagePoolStableHash = "stable_hash"
)

type MessageTemplateConfig struct {
	Tag      string
	Template string
}

type MessagePoolConfig struct {
	Tag       string
	Strategy  string
	Templates []MessageTemplateConfig
}

const (
	EmployerRuleMessagePool = "message_pool"
	EmployerRuleModel       = "model"
	EmployerRuleSkip        = "skip"
	EmployerRuleReview      = "review"
)

type EmployerRuleConfig struct {
	EmployerGroups []string
	Action         string
	MessagePool    *MessagePoolConfig
	Model          *ApplicationModelConfig
}

type compiledMessageTemplate struct {
	tag      string
	template *template.Template
}

type RuleTemplatePreparer struct {
	includeAny    []string
	excludeAny    []string
	staticMessage string
	template      *template.Template
	messagePool   *compiledMessagePool
	model         *compiledApplicationModel
	resume        *ApplicationResumeContext
	employerMatch *EmployerGroupMatcher
	employerRules []compiledEmployerRule
}

type compiledMessagePool struct {
	tag       string
	strategy  string
	templates []compiledMessageTemplate
}

type compiledEmployerRule struct {
	groups      []string
	action      string
	messagePool *compiledMessagePool
	model       *compiledApplicationModel
}

type renderedApplicationMessage struct {
	text       string
	selection  string
	provenance core.ApplicationPreparationProvenance
}

type ApplicationTemplateData struct {
	ApplicationID string                    `json:"application_id"`
	ProfileID     string                    `json:"profile_id"`
	Vacancy       ApplicationVacancyContext `json:"vacancy"`
	Resume        *ApplicationResumeContext `json:"resume,omitempty"`

	// Flat fields are retained for existing templates. New templates should use
	// Vacancy so the same structured context can later be passed to a model
	// operator without inventing a second schema.
	Title       string   `json:"-"`
	Employer    string   `json:"-"`
	URL         string   `json:"-"`
	Description string   `json:"-"`
	KeySkills   []string `json:"-"`
}

type ApplicationResumeContext struct {
	ResumeID string         `json:"resume_id"`
	FactsTag string         `json:"facts_tag"`
	Digest   string         `json:"digest"`
	Facts    map[string]any `json:"facts"`
}

func (resume ApplicationResumeContext) Validate() error {
	if strings.TrimSpace(resume.ResumeID) == "" || strings.TrimSpace(resume.FactsTag) == "" || len(resume.Facts) == 0 {
		return errors.New("application resume context requires resume id, facts tag and facts")
	}
	encoded := strings.TrimPrefix(strings.TrimSpace(resume.Digest), "sha256:")
	digest, err := hex.DecodeString(encoded)
	if err != nil || len(digest) != sha256.Size || !strings.HasPrefix(resume.Digest, "sha256:") {
		return errors.New("application resume context requires a sha256 digest")
	}
	return nil
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
	messageSources := 0
	if strings.TrimSpace(config.StaticMessage) != "" {
		messageSources++
	}
	if strings.TrimSpace(config.MessageTemplate) != "" {
		messageSources++
	}
	if config.MessagePool != nil {
		messageSources++
	}
	if messageSources > 1 {
		return nil, errors.New("application operator accepts one static message, message template or message pool")
	}
	resume, err := cloneApplicationResumeContext(config.Resume)
	if err != nil {
		return nil, err
	}
	var compiled *template.Template
	if strings.TrimSpace(config.MessageTemplate) != "" {
		compiled, err = compileApplicationTemplate("cover-letter", config.MessageTemplate, resume)
		if err != nil {
			return nil, err
		}
	}
	messagePool, err := compileMessagePool(config.MessagePool, resume)
	if err != nil {
		return nil, err
	}
	model, err := compileApplicationModel(config.Model)
	if err != nil {
		return nil, err
	}
	if model != nil && messageSources != 1 {
		return nil, errors.New("application model requires exactly one configured message fallback")
	}
	if model != nil && resume == nil {
		return nil, errors.New("application model requires explicit resume facts")
	}
	employerRules, err := compileEmployerRules(config.EmployerMatcher, config.EmployerRules, resume)
	if err != nil {
		return nil, err
	}
	preparer := &RuleTemplatePreparer{
		includeAny: includeAny, excludeAny: excludeAny,
		staticMessage: strings.TrimSpace(config.StaticMessage), template: compiled, messagePool: messagePool, model: model, resume: resume,
		employerMatch: config.EmployerMatcher, employerRules: employerRules,
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
	selectedPool := preparer.messagePool
	selectedModel := preparer.model
	employerReason := ""
	for _, rule := range preparer.employerRules {
		match, matched := preparer.employerMatch.MatchAny(vacancy, rule.groups)
		if !matched {
			continue
		}
		employerReason = employerMatchReason(match)
		switch rule.action {
		case EmployerRuleSkip:
			return ApplicationPreparation{Outcome: ApplicationSkip, Code: "employer_rule_skip", Reason: employerReason}, nil
		case EmployerRuleReview:
			return ApplicationPreparation{Outcome: ApplicationReview, Code: "employer_rule_review", Reason: employerReason}, nil
		case EmployerRuleMessagePool:
			selectedPool = rule.messagePool
			selectedModel = nil
		case EmployerRuleModel:
			selectedPool = rule.messagePool
			selectedModel = rule.model
		}
		break
	}
	rendered, err := preparer.renderMessage(ctx, application, vacancy, selectedPool, selectedModel)
	if err != nil {
		return ApplicationPreparation{}, err
	}
	reason := "vacancy passed deterministic rules"
	if employerReason != "" {
		reason += "; " + employerReason
	}
	if rendered.selection != "" {
		reason += "; " + rendered.selection
	}
	result := ApplicationPreparation{
		Outcome: ApplicationApply, Code: "qualified",
		Reason: reason, Message: rendered.text, Provenance: rendered.provenance,
	}
	if err := result.Validate(); err != nil {
		return ApplicationPreparation{}, err
	}
	return result, nil
}

func (preparer *RuleTemplatePreparer) renderMessage(ctx context.Context, application core.Application, vacancy core.Vacancy, messagePool *compiledMessagePool, model *compiledApplicationModel) (renderedApplicationMessage, error) {
	if model != nil {
		response, inputDigest, err := model.generate(ctx, application, vacancy, preparer.resume)
		if err == nil {
			name := strings.TrimSpace(response.Model)
			if name == "" {
				name = "provider-selected"
			}
			return renderedApplicationMessage{
				text: response.Text, selection: fmt.Sprintf("model %q generated with %q prompt %q", model.tag, name, model.promptVersion),
				provenance: core.ApplicationPreparationProvenance{
					Version: core.ApplicationPreparationProvenanceVersion, Source: core.ApplicationPreparationSourceModel,
					OperatorTag: model.tag, OperatorVersion: model.promptVersion, Model: name, ProviderResponseID: strings.TrimSpace(response.ResponseID),
					ResumeFactsTag: preparer.resume.FactsTag, ResumeFactsDigest: preparer.resume.Digest,
					InputDigest: inputDigest, OutputDigest: applicationTextDigest(response.Text),
				},
			}, nil
		}
		if ctx.Err() != nil {
			return renderedApplicationMessage{}, ctx.Err()
		}
		fallback, fallbackErr := preparer.renderConfiguredMessage(application, vacancy, messagePool)
		if fallbackErr != nil {
			return renderedApplicationMessage{}, fallbackErr
		}
		selection := fallback.selection
		if selection == "" {
			selection = "configured message fallback"
		}
		fallback.provenance = core.ApplicationPreparationProvenance{
			Version: core.ApplicationPreparationProvenanceVersion, Source: core.ApplicationPreparationSourceModelFallback,
			OperatorTag: model.tag, OperatorVersion: model.promptVersion, FailureKind: string(ModelFailureKindOf(err)),
			FallbackSource: fallback.provenance.Source, MessagePoolTag: fallback.provenance.MessagePoolTag, TemplateTag: fallback.provenance.TemplateTag,
			ResumeFactsTag: preparer.resume.FactsTag, ResumeFactsDigest: preparer.resume.Digest,
			InputDigest: inputDigest, OutputDigest: applicationTextDigest(fallback.text),
		}
		fallback.selection = fmt.Sprintf("model %q fallback after %s; %s", model.tag, ModelFailureKindOf(err), selection)
		return fallback, nil
	}
	return preparer.renderConfiguredMessage(application, vacancy, messagePool)
}

func (preparer *RuleTemplatePreparer) renderConfiguredMessage(application core.Application, vacancy core.Vacancy, messagePool *compiledMessagePool) (renderedApplicationMessage, error) {
	if messagePool != nil {
		selected := messagePool.templates[messagePool.index(application)]
		message, err := executeApplicationTemplate(selected.template, application, vacancy, preparer.resume)
		if err != nil {
			return renderedApplicationMessage{}, err
		}
		return renderedApplicationMessage{
			text: message, selection: fmt.Sprintf("message pool %q selected template %q", messagePool.tag, selected.tag),
			provenance: core.ApplicationPreparationProvenance{
				Version: core.ApplicationPreparationProvenanceVersion, Source: core.ApplicationPreparationSourceMessagePool,
				MessagePoolTag: messagePool.tag, TemplateTag: selected.tag, OutputDigest: applicationTextDigest(message),
			},
		}, nil
	}
	if preparer.template == nil {
		return renderedApplicationMessage{
			text: preparer.staticMessage,
			provenance: core.ApplicationPreparationProvenance{
				Version: core.ApplicationPreparationProvenanceVersion, Source: core.ApplicationPreparationSourceStatic,
				OutputDigest: applicationTextDigest(preparer.staticMessage),
			},
		}, nil
	}
	message, err := executeApplicationTemplate(preparer.template, application, vacancy, preparer.resume)
	if err != nil {
		return renderedApplicationMessage{}, err
	}
	return renderedApplicationMessage{
		text: message,
		provenance: core.ApplicationPreparationProvenance{
			Version: core.ApplicationPreparationProvenanceVersion, Source: core.ApplicationPreparationSourceTemplate,
			TemplateTag: "inline", OutputDigest: applicationTextDigest(message),
		},
	}, nil
}

func compileEmployerRules(matcher *EmployerGroupMatcher, configs []EmployerRuleConfig, resume *ApplicationResumeContext) ([]compiledEmployerRule, error) {
	if len(configs) == 0 {
		return nil, nil
	}
	if matcher == nil {
		return nil, errors.New("employer rules require employer group matcher")
	}
	result := make([]compiledEmployerRule, 0, len(configs))
	for index, config := range configs {
		if len(config.EmployerGroups) == 0 {
			return nil, fmt.Errorf("employer rule %d requires at least one group", index)
		}
		groups := make([]string, 0, len(config.EmployerGroups))
		seen := make(map[string]struct{}, len(config.EmployerGroups))
		for _, value := range config.EmployerGroups {
			tag := strings.TrimSpace(value)
			if !matcher.HasGroup(tag) {
				return nil, fmt.Errorf("employer rule %d references unknown group %q", index, tag)
			}
			if _, exists := seen[tag]; exists {
				return nil, fmt.Errorf("employer rule %d contains duplicate group %q", index, tag)
			}
			seen[tag] = struct{}{}
			groups = append(groups, tag)
		}
		action := strings.TrimSpace(config.Action)
		switch action {
		case EmployerRuleSkip, EmployerRuleReview:
			if config.MessagePool != nil || config.Model != nil {
				return nil, fmt.Errorf("employer rule %d action %q cannot use a message pool or model", index, action)
			}
		case EmployerRuleMessagePool:
			if config.MessagePool == nil || config.Model != nil {
				return nil, fmt.Errorf("employer rule %d action %q requires a message pool", index, action)
			}
		case EmployerRuleModel:
			if config.MessagePool == nil || config.Model == nil {
				return nil, fmt.Errorf("employer rule %d action %q requires a model and message pool fallback", index, action)
			}
		default:
			return nil, fmt.Errorf("employer rule %d has unsupported action %q", index, action)
		}
		pool, err := compileMessagePool(config.MessagePool, resume)
		if err != nil {
			return nil, fmt.Errorf("employer rule %d: %w", index, err)
		}
		model, err := compileApplicationModel(config.Model)
		if err != nil {
			return nil, fmt.Errorf("employer rule %d: %w", index, err)
		}
		if model != nil && resume == nil {
			return nil, fmt.Errorf("employer rule %d: application model requires explicit resume facts", index)
		}
		result = append(result, compiledEmployerRule{groups: groups, action: action, messagePool: pool, model: model})
	}
	return result, nil
}

func employerMatchReason(match EmployerGroupMatch) string {
	reason := fmt.Sprintf("employer rule matched group %q by %s", match.GroupTag, match.Evidence.Kind)
	if match.Evidence.Via != "" {
		reason += fmt.Sprintf(" via %q", match.Evidence.Via)
	}
	return reason
}

func executeApplicationTemplate(compiled *template.Template, application core.Application, vacancy core.Vacancy, resume *ApplicationResumeContext) (string, error) {
	data := newApplicationTemplateData(application, vacancy, resume)
	var output bytes.Buffer
	if err := compiled.Execute(&output, data); err != nil {
		return "", fmt.Errorf("render cover letter template: %w", err)
	}
	return strings.TrimSpace(output.String()), nil
}

func compileApplicationTemplate(name, source string, resume *ApplicationResumeContext) (*template.Template, error) {
	compiled, err := template.New(name).Option("missingkey=error").Parse(source)
	if err != nil {
		return nil, fmt.Errorf("parse cover letter template %q: %w", name, err)
	}
	var probe bytes.Buffer
	if err := compiled.Execute(&probe, ApplicationTemplateData{Vacancy: ApplicationVacancyContext{Attributes: map[string]any{}}, Resume: resume}); err != nil {
		return nil, fmt.Errorf("validate cover letter template %q: %w", name, err)
	}
	if !utf8.ValidString(probe.String()) || utf8.RuneCountInString(probe.String()) > maximumApplicationMessageRunes {
		return nil, fmt.Errorf("cover letter template %q output must be valid UTF-8 and at most 10000 characters", name)
	}
	return compiled, nil
}

func compileMessagePool(config *MessagePoolConfig, resume *ApplicationResumeContext) (*compiledMessagePool, error) {
	if config == nil {
		return nil, nil
	}
	tag := strings.TrimSpace(config.Tag)
	if tag == "" {
		return nil, errors.New("message pool requires tag")
	}
	strategy := strings.TrimSpace(config.Strategy)
	if strategy == "" {
		strategy = MessagePoolFirst
	}
	if strategy != MessagePoolFirst && strategy != MessagePoolStableHash {
		return nil, fmt.Errorf("message pool %q has unsupported strategy %q", tag, strategy)
	}
	if len(config.Templates) == 0 {
		return nil, fmt.Errorf("message pool %q requires at least one template", tag)
	}
	result := &compiledMessagePool{tag: tag, strategy: strategy, templates: make([]compiledMessageTemplate, 0, len(config.Templates))}
	seen := make(map[string]struct{}, len(config.Templates))
	for _, candidate := range config.Templates {
		candidateTag := strings.TrimSpace(candidate.Tag)
		if candidateTag == "" {
			return nil, fmt.Errorf("message pool %q contains a template without tag", tag)
		}
		if _, exists := seen[candidateTag]; exists {
			return nil, fmt.Errorf("message pool %q contains duplicate template tag %q", tag, candidateTag)
		}
		seen[candidateTag] = struct{}{}
		if strings.TrimSpace(candidate.Template) == "" {
			return nil, fmt.Errorf("message pool %q template %q is empty", tag, candidateTag)
		}
		compiled, err := compileApplicationTemplate(tag+"/"+candidateTag, candidate.Template, resume)
		if err != nil {
			return nil, err
		}
		result.templates = append(result.templates, compiledMessageTemplate{tag: candidateTag, template: compiled})
	}
	return result, nil
}

func (pool *compiledMessagePool) index(application core.Application) int {
	if pool.strategy == MessagePoolFirst || len(pool.templates) == 1 {
		return 0
	}
	digest := sha256.Sum256([]byte(string(application.Key.ProfileID) + "\x00" + string(application.Key.Vacancy.Platform) + "\x00" + application.Key.Vacancy.ExternalID))
	return int(binary.BigEndian.Uint64(digest[:8]) % uint64(len(pool.templates)))
}

func NewApplicationTemplateData(application core.Application, vacancy core.Vacancy) ApplicationTemplateData {
	return newApplicationTemplateData(application, vacancy, nil)
}

func newApplicationTemplateData(application core.Application, vacancy core.Vacancy, resume *ApplicationResumeContext) ApplicationTemplateData {
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
		ApplicationID: string(application.ID), ProfileID: string(application.Key.ProfileID), Vacancy: context, Resume: copyApplicationResumeContext(resume),
		Title: context.Title, Employer: context.Employer, URL: context.URL,
		Description: context.Description, KeySkills: append([]string(nil), context.KeySkills...),
	}
}

func cloneApplicationResumeContext(source *ApplicationResumeContext) (*ApplicationResumeContext, error) {
	if source == nil {
		return nil, nil
	}
	if err := source.Validate(); err != nil {
		return nil, err
	}
	return copyApplicationResumeContext(source), nil
}

func copyApplicationResumeContext(source *ApplicationResumeContext) *ApplicationResumeContext {
	if source == nil {
		return nil
	}
	result := *source
	result.Facts = cloneVacancyAttributes(source.Facts)
	return &result
}

func applicationBytesDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func applicationTextDigest(value string) string {
	return applicationBytesDigest([]byte(value))
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
