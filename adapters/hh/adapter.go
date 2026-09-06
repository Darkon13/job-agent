package hh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

const Name = "hh"

type Config struct {
	APIDelayMS int    `json:"api_delay_ms,omitempty"`
	UserAgent  string `json:"user_agent,omitempty"`
}

type SearchSource string

const (
	SearchSourceGlobal         SearchSource = "global"
	SearchSourceSimilarResume  SearchSource = "similar_resume"
	SearchSourceSimilarVacancy SearchSource = "similar_vacancy"
	SearchSourceRelatedVacancy SearchSource = "related_vacancy"
)

// SearchQuery belongs to the HH adapter because platform filters are not part
// of the common domain contract.
type SearchQuery struct {
	Source             SearchSource `json:"source"`
	Resume             string       `json:"resume,omitempty"`
	Vacancy            string       `json:"vacancy,omitempty"`
	Text               string       `json:"text,omitempty"`
	SearchField        []string     `json:"search_field,omitempty"`
	Experience         []string     `json:"experience,omitempty"`
	Employment         []string     `json:"employment,omitempty"`
	Schedule           []string     `json:"schedule,omitempty"`
	Area               []string     `json:"area,omitempty"`
	Metro              []string     `json:"metro,omitempty"`
	ProfessionalRole   []string     `json:"professional_role,omitempty"`
	Industry           []string     `json:"industry,omitempty"`
	EmployerID         []string     `json:"employer_id,omitempty"`
	ExcludedEmployerID []string     `json:"excluded_employer_id,omitempty"`
	Currency           string       `json:"currency,omitempty"`
	Salary             *int         `json:"salary,omitempty"`
	Label              []string     `json:"label,omitempty"`
	OnlyWithSalary     bool         `json:"only_with_salary,omitempty"`
	Period             *int         `json:"period,omitempty"`
	DateFrom           string       `json:"date_from,omitempty"`
	DateTo             string       `json:"date_to,omitempty"`
	TopLat             *float64     `json:"top_lat,omitempty"`
	BottomLat          *float64     `json:"bottom_lat,omitempty"`
	LeftLng            *float64     `json:"left_lng,omitempty"`
	RightLng           *float64     `json:"right_lng,omitempty"`
	OrderBy            string       `json:"order_by,omitempty"`
	SortPointLat       *float64     `json:"sort_point_lat,omitempty"`
	SortPointLng       *float64     `json:"sort_point_lng,omitempty"`
	Clusters           bool         `json:"clusters,omitempty"`
	DescribeArguments  bool         `json:"describe_arguments,omitempty"`
	NoMagic            bool         `json:"no_magic,omitempty"`
	Premium            bool         `json:"premium,omitempty"`
	ResponsesCount     bool         `json:"responses_count_enabled,omitempty"`
	PartTime           []string     `json:"part_time,omitempty"`
	AcceptTemporary    *bool        `json:"accept_temporary,omitempty"`
	EmploymentForm     []string     `json:"employment_form,omitempty"`
	WorkScheduleByDays []string     `json:"work_schedule_by_days,omitempty"`
	WorkingHours       []string     `json:"working_hours,omitempty"`
	WorkFormat         []string     `json:"work_format,omitempty"`
	ExcludedText       string       `json:"excluded_text,omitempty"`
	Education          []string     `json:"education,omitempty"`
	PageSize           int          `json:"page_size,omitempty"`
	MaxPages           int          `json:"max_pages,omitempty"`
}

type Adapter struct {
	config         Config
	mu             sync.RWMutex
	clients        map[core.ProfileID]*ReadClient
	browserClients map[core.ProfileID]*BrowserReadClient
}

var _ adapter.Adapter = (*Adapter)(nil)
var _ adapter.ConversationTransport = (*Adapter)(nil)
var _ adapter.ProfileReaderFactory = (*Adapter)(nil)
var _ adapter.BrowserSessionBinder = (*Adapter)(nil)
var _ adapter.VacancyReader = (*Adapter)(nil)
var _ adapter.SuitableResumeReader = (*Adapter)(nil)
var _ adapter.ApplicationTransport = (*Adapter)(nil)
var _ adapter.ApplicationReconciler = (*Adapter)(nil)

func New(raw json.RawMessage) (adapter.Adapter, error) {
	var cfg Config
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("decode hh config: %w", err)
		}
	}
	return &Adapter{
		config: cfg, clients: make(map[core.ProfileID]*ReadClient),
		browserClients: make(map[core.ProfileID]*BrowserReadClient),
	}, nil
}

func (a *Adapter) Name() string { return Name }

func (a *Adapter) NewProfileReader(profileID core.ProfileID, credentialsRef string) (adapter.ProfileReader, error) {
	client, err := NewReadClient(profileID, credentialsRef, a.config.UserAgent, nil)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if existing, exists := a.clients[profileID]; exists {
		if existing.credentialsRef != client.credentialsRef {
			return nil, fmt.Errorf("HH profile %s is already bound to another credentials_ref", profileID)
		}
		return existing, nil
	}
	a.clients[profileID] = client
	return client, nil
}

func (a *Adapter) BindBrowserSession(profileID core.ProfileID, stateFile string) (adapter.VacancyReader, error) {
	client, err := NewBrowserReadClient(profileID, stateFile, a.config.UserAgent, nil)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if existing := a.browserClients[profileID]; existing != nil {
		if existing.stateFile != client.stateFile {
			return nil, fmt.Errorf("HH profile %s is already bound to another browser state", profileID)
		}
		return existing, nil
	}
	a.browserClients[profileID] = client
	return client, nil
}

func (a *Adapter) Capabilities() []core.Capability {
	return []core.Capability{
		core.CapabilitySearchVacancies,
		core.CapabilityGetVacancy,
		core.CapabilityApply,
		core.CapabilityQuestionnaire,
		core.CapabilityConversationRead,
		core.CapabilityConversationWrite,
	}
}

func (a *Adapter) ValidateSearch(raw json.RawMessage) error {
	var query SearchQuery
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&query); err != nil {
		return fmt.Errorf("decode hh search query: %w", err)
	}
	switch query.Source {
	case SearchSourceGlobal:
		if query.Resume != "" || query.Vacancy != "" {
			return errors.New("hh global search must not specify resume or vacancy")
		}
	case SearchSourceSimilarResume:
		if query.Resume == "" {
			return errors.New("hh similar_resume search requires resume")
		}
		if query.Vacancy != "" {
			return errors.New("hh similar_resume search must not specify vacancy")
		}
	case SearchSourceSimilarVacancy, SearchSourceRelatedVacancy:
		if query.Vacancy == "" {
			return fmt.Errorf("hh %s search requires vacancy", query.Source)
		}
		if query.Resume != "" {
			return fmt.Errorf("hh %s search must not specify resume", query.Source)
		}
	default:
		return fmt.Errorf("unsupported hh search source %q", query.Source)
	}
	if query.Period != nil && (*query.Period <= 0 || *query.Period > 30) {
		return errors.New("hh period must be between 1 and 30 days")
	}
	if query.Salary != nil && *query.Salary < 0 {
		return errors.New("hh salary must not be negative")
	}
	if query.PageSize < 0 || query.PageSize > searchPageSize {
		return fmt.Errorf("hh page_size must be between 1 and %d when set", searchPageSize)
	}
	if query.MaxPages < 0 || query.MaxPages > searchMaximumDepth/searchPageSize {
		return fmt.Errorf("hh max_pages must be between 1 and %d when set", searchMaximumDepth/searchPageSize)
	}
	geoFields := 0
	for _, value := range []*float64{query.TopLat, query.BottomLat, query.LeftLng, query.RightLng} {
		if value != nil {
			geoFields++
		}
	}
	if geoFields != 0 && geoFields != 4 {
		return errors.New("hh geo bounding box requires top_lat, bottom_lat, left_lng and right_lng together")
	}
	if (query.SortPointLat == nil) != (query.SortPointLng == nil) {
		return errors.New("hh sort point requires sort_point_lat and sort_point_lng together")
	}
	if query.OrderBy == "distance" && query.SortPointLat == nil {
		return errors.New("hh distance ordering requires a sort point")
	}
	if query.Period != nil && (query.DateFrom != "" || query.DateTo != "") {
		return errors.New("hh period cannot be combined with date_from or date_to")
	}
	for field, values := range map[string][]string{
		"search_field": query.SearchField, "experience": query.Experience,
		"employment": query.Employment, "schedule": query.Schedule, "area": query.Area,
		"metro": query.Metro, "professional_role": query.ProfessionalRole, "industry": query.Industry,
		"employer_id": query.EmployerID, "excluded_employer_id": query.ExcludedEmployerID,
		"label": query.Label, "part_time": query.PartTime, "employment_form": query.EmploymentForm,
		"work_schedule_by_days": query.WorkScheduleByDays, "working_hours": query.WorkingHours,
		"work_format": query.WorkFormat, "education": query.Education,
	} {
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("hh %s contains an empty value", field)
			}
			if _, exists := seen[value]; exists {
				return fmt.Errorf("hh %s contains duplicate value %q", field, value)
			}
			seen[value] = struct{}{}
		}
	}
	if err := validateHHSearchTime("date_from", query.DateFrom); err != nil {
		return err
	}
	if err := validateHHSearchTime("date_to", query.DateTo); err != nil {
		return err
	}
	if query.TopLat != nil {
		if *query.TopLat < -90 || *query.TopLat > 90 || *query.BottomLat < -90 || *query.BottomLat > 90 || *query.TopLat < *query.BottomLat {
			return errors.New("hh latitude bounds are invalid")
		}
		if *query.LeftLng < -180 || *query.LeftLng > 180 || *query.RightLng < -180 || *query.RightLng > 180 || *query.LeftLng > *query.RightLng {
			return errors.New("hh longitude bounds are invalid")
		}
	}
	if query.SortPointLat != nil && (*query.SortPointLat < -90 || *query.SortPointLat > 90 || *query.SortPointLng < -180 || *query.SortPointLng > 180) {
		return errors.New("hh sort point is invalid")
	}
	return nil
}

func validateHHSearchTime(field, value string) error {
	if value == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05-0700", time.DateOnly} {
		if _, err := time.Parse(layout, value); err == nil {
			return nil
		}
	}
	return fmt.Errorf("hh %s must be an ISO-8601 date or timestamp", field)
}

func (a *Adapter) Search(ctx context.Context, profileID core.ProfileID, raw json.RawMessage, cursor string) (core.SearchPage, error) {
	if err := a.ValidateSearch(raw); err != nil {
		return core.SearchPage{}, err
	}
	var query SearchQuery
	if err := json.Unmarshal(raw, &query); err != nil {
		return core.SearchPage{}, fmt.Errorf("decode hh search query: %w", err)
	}
	if query.Source != SearchSourceGlobal {
		return core.SearchPage{}, hhUnsupported("vacancies.search." + string(query.Source))
	}
	a.mu.RLock()
	client := a.clients[profileID]
	browserClient := a.browserClients[profileID]
	a.mu.RUnlock()
	if client != nil {
		return client.SearchGlobal(ctx, query, cursor)
	}
	if browserClient != nil {
		return browserClient.SearchGlobal(ctx, query, cursor)
	}
	return core.SearchPage{}, operationError(core.ErrorUnauthorized, "vacancies.search.global", "HH profile has no bound read session", nil)
}

func (a *Adapter) ReadVacancy(ctx context.Context, profileID core.ProfileID, key core.VacancyKey) (core.Vacancy, error) {
	if key.Platform != Name {
		return core.Vacancy{}, errors.New("HH vacancy reader received another platform")
	}
	a.mu.RLock()
	client := a.clients[profileID]
	browserClient := a.browserClients[profileID]
	a.mu.RUnlock()
	if client != nil {
		return client.ReadVacancy(ctx, profileID, key)
	}
	if browserClient != nil {
		return browserClient.ReadVacancy(ctx, profileID, key)
	}
	return core.Vacancy{}, operationError(core.ErrorUnauthorized, "vacancies.read", "HH profile has no bound read session", nil)
}

func (a *Adapter) ListSuitableResumes(ctx context.Context, profileID core.ProfileID, key core.VacancyKey) ([]adapter.SuitableResume, error) {
	if key.Platform != Name {
		return nil, errors.New("HH suitable resume reader received another platform")
	}
	a.mu.RLock()
	client := a.clients[profileID]
	a.mu.RUnlock()
	if client == nil {
		return nil, operationError(core.ErrorUnauthorized, "vacancies.suitable_resumes", "HH profile has no bound credentials", nil)
	}
	return client.ListSuitableResumes(ctx, profileID, key)
}

func (a *Adapter) SubmitApplication(ctx context.Context, command adapter.ApplicationSubmitCommand) (adapter.ApplicationSubmitResult, error) {
	a.mu.RLock()
	client := a.clients[command.ProfileID]
	a.mu.RUnlock()
	if client == nil {
		return adapter.ApplicationSubmitResult{}, operationError(core.ErrorUnauthorized, "applications.submit", "HH profile has no bound credentials", nil)
	}
	return client.SubmitApplication(ctx, command)
}

func (a *Adapter) ReconcileApplication(ctx context.Context, command adapter.ApplicationReconcileCommand) (adapter.ApplicationReconcileResult, error) {
	a.mu.RLock()
	client := a.clients[command.ProfileID]
	a.mu.RUnlock()
	if client == nil {
		return adapter.ApplicationReconcileResult{}, operationError(core.ErrorUnauthorized, "applications.reconcile", "HH profile is not bound to OAuth credentials", nil)
	}
	return client.ReconcileApplication(ctx, command)
}

func (a *Adapter) SendConversationMessage(context.Context, adapter.ConversationSendCommand) (core.ConversationMessage, error) {
	return core.ConversationMessage{}, hhUnsupported("conversations.send")
}

func (a *Adapter) MarkConversationRead(context.Context, core.ProfileID, string) error {
	return hhUnsupported("conversations.mark_read")
}

func (a *Adapter) SyncConversation(context.Context, core.ProfileID, core.ConversationID, string) (adapter.ConversationSyncResult, error) {
	return adapter.ConversationSyncResult{}, hhUnsupported("conversations.sync")
}

func hhUnsupported(operation string) error {
	return &core.OperationError{
		Category: core.ErrorUnsupported, Operation: operation, Platform: Name,
		Message: "HH transport operation is not implemented",
	}
}
