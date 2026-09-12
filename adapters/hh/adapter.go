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
	config                     Config
	mu                         sync.RWMutex
	clients                    map[core.ProfileID]*ReadClient
	browserClients             map[core.ProfileID]*BrowserReadClient
	browserApplicationClients  map[core.ProfileID]*BrowserApplicationClient
	browserConversationClients map[core.ProfileID]*BrowserConversationClient
	browserProfileStateClients map[core.ProfileID]*BrowserProfileStateClient
}

var _ adapter.Adapter = (*Adapter)(nil)
var _ adapter.ProfileReaderFactory = (*Adapter)(nil)
var _ adapter.BrowserSessionBinder = (*Adapter)(nil)
var _ adapter.BrowserApplicationSessionBinder = (*Adapter)(nil)
var _ adapter.BrowserConversationSessionBinder = (*Adapter)(nil)
var _ adapter.BrowserProfileStateSessionBinder = (*Adapter)(nil)
var _ adapter.VacancyReader = (*Adapter)(nil)
var _ adapter.ProfileStateReader = (*Adapter)(nil)
var _ adapter.ProfileStateWriter = (*Adapter)(nil)
var _ adapter.SuitableResumeReader = (*Adapter)(nil)
var _ adapter.ApplicationTransport = (*Adapter)(nil)
var _ adapter.ApplicationReconciler = (*Adapter)(nil)
var _ adapter.ApplicationStateObserver = (*Adapter)(nil)
var _ adapter.ResumePublisher = (*Adapter)(nil)
var _ adapter.VacancyTestCapturer = (*Adapter)(nil)
var _ adapter.VacancyTestSubmitter = (*Adapter)(nil)

func New(raw json.RawMessage) (adapter.Adapter, error) {
	var cfg Config
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("decode hh config: %w", err)
		}
	}
	return &Adapter{
		config: cfg, clients: make(map[core.ProfileID]*ReadClient),
		browserClients:             make(map[core.ProfileID]*BrowserReadClient),
		browserApplicationClients:  make(map[core.ProfileID]*BrowserApplicationClient),
		browserConversationClients: make(map[core.ProfileID]*BrowserConversationClient),
		browserProfileStateClients: make(map[core.ProfileID]*BrowserProfileStateClient),
	}, nil
}

func (a *Adapter) BindBrowserConversationSession(profileID core.ProfileID, stateFile string, options adapter.BrowserConversationOptions) (adapter.ConversationTransport, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	reader := a.browserClients[profileID]
	if reader == nil {
		var err error
		reader, err = NewBrowserReadClient(profileID, stateFile, a.config.UserAgent, nil)
		if err != nil {
			return nil, err
		}
		a.browserClients[profileID] = reader
	} else if reader.stateFile != stateFile {
		return nil, fmt.Errorf("HH profile %s is already bound to another browser state", profileID)
	}
	if existing := a.browserConversationClients[profileID]; existing != nil {
		if existing.options != options {
			return nil, fmt.Errorf("HH profile %s is already bound with different browser conversation permissions", profileID)
		}
		return existing, nil
	}
	client := newBrowserConversationClient(reader, options)
	a.browserConversationClients[profileID] = client
	return client, nil
}

func (a *Adapter) BindBrowserProfileStateSession(profileID core.ProfileID, stateFile string) (adapter.ProfileStateWriter, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	reader := a.browserClients[profileID]
	if reader == nil {
		var err error
		reader, err = NewBrowserReadClient(profileID, stateFile, a.config.UserAgent, nil)
		if err != nil {
			return nil, err
		}
		a.browserClients[profileID] = reader
	} else if reader.stateFile != stateFile {
		return nil, fmt.Errorf("HH profile %s is already bound to another browser state", profileID)
	}
	if existing := a.browserProfileStateClients[profileID]; existing != nil {
		return existing, nil
	}
	client := newBrowserProfileStateClient(reader)
	a.browserProfileStateClients[profileID] = client
	return client, nil
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

func (a *Adapter) BindBrowserApplicationSession(profileID core.ProfileID, stateFile string, options adapter.BrowserApplicationOptions) (adapter.ApplicationTransport, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	reader := a.browserClients[profileID]
	if reader == nil {
		var err error
		reader, err = NewBrowserReadClient(profileID, stateFile, a.config.UserAgent, nil)
		if err != nil {
			return nil, err
		}
		a.browserClients[profileID] = reader
	} else if reader.stateFile != stateFile {
		return nil, fmt.Errorf("HH profile %s is already bound to another browser state", profileID)
	}
	if existing := a.browserApplicationClients[profileID]; existing != nil {
		if existing.options != options {
			return nil, fmt.Errorf("HH profile %s is already bound with different browser application permissions", profileID)
		}
		return existing, nil
	}
	client := newBrowserApplicationClient(reader, options)
	a.browserApplicationClients[profileID] = client
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
		core.CapabilityConversationMarkRead,
		core.CapabilityResumeRead,
		core.CapabilityResumeUpdate,
		core.CapabilityResumePublish,
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
	case SearchSourceSimilarVacancy:
		if query.Vacancy == "" {
			return errors.New("hh similar_vacancy search requires vacancy")
		}
		if query.Resume != "" {
			return errors.New("hh similar_vacancy search must not specify resume")
		}
	case SearchSourceRelatedVacancy:
		if query.Vacancy == "" {
			return errors.New("hh related_vacancy search requires vacancy")
		}
		if query.Resume != "" {
			return errors.New("hh related_vacancy search must not specify resume")
		}
		if query.hasSearchFilters() {
			return errors.New("hh related_vacancy search accepts only pagination")
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

func (a *Adapter) CaptureVacancyTest(ctx context.Context, profileID core.ProfileID, key core.VacancyKey) (core.Questionnaire, error) {
	a.mu.RLock()
	browserClient := a.browserClients[profileID]
	a.mu.RUnlock()
	if browserClient == nil {
		return core.Questionnaire{}, operationError(core.ErrorUnsupported, "vacancies.test.capture", "HH profile has no browser read session", nil)
	}
	return browserClient.CaptureVacancyTest(ctx, profileID, key)
}

func (a *Adapter) SubmitVacancyTest(ctx context.Context, profileID core.ProfileID, key core.VacancyKey, answers []core.ResolvedAnswer) error {
	a.mu.RLock()
	browserClient := a.browserApplicationClients[profileID]
	a.mu.RUnlock()
	if browserClient == nil {
		return operationError(core.ErrorUnsupported, "vacancies.test.submit", "HH profile has no browser application session", nil)
	}
	return browserClient.SubmitVacancyTest(ctx, profileID, key, answers)
}

func (a *Adapter) PublishResume(ctx context.Context, command adapter.ResumePublishCommand) (adapter.ResumePublishResult, error) {
	a.mu.RLock()
	client := a.clients[command.ProfileID]
	a.mu.RUnlock()
	if client == nil {
		return adapter.ResumePublishResult{}, operationError(core.ErrorUnsupported, "resumes.publish", "HH profile has no API session for resume publish", nil)
	}
	return client.PublishResume(ctx, command)
}

func (query SearchQuery) hasModernWorkFields() bool {
	return len(query.EmploymentForm) != 0 || len(query.WorkScheduleByDays) != 0 ||
		len(query.WorkingHours) != 0 || len(query.WorkFormat) != 0
}

func (query SearchQuery) hasSearchFilters() bool {
	return query.Text != "" || len(query.SearchField) != 0 || len(query.Experience) != 0 ||
		len(query.Employment) != 0 || len(query.Schedule) != 0 || len(query.Area) != 0 ||
		len(query.Metro) != 0 || len(query.ProfessionalRole) != 0 || len(query.Industry) != 0 ||
		len(query.EmployerID) != 0 || len(query.ExcludedEmployerID) != 0 || query.Currency != "" ||
		query.Salary != nil || len(query.Label) != 0 || query.OnlyWithSalary ||
		query.Period != nil || query.DateFrom != "" || query.DateTo != "" ||
		query.TopLat != nil || query.BottomLat != nil || query.LeftLng != nil || query.RightLng != nil ||
		query.OrderBy != "" || query.SortPointLat != nil || query.SortPointLng != nil ||
		query.Clusters || query.DescribeArguments || query.NoMagic || query.Premium ||
		query.ResponsesCount || len(query.PartTime) != 0 || query.AcceptTemporary != nil ||
		query.ExcludedText != "" || len(query.Education) != 0 || query.hasModernWorkFields()
}

// SupportsSearch reports whether the transports bound to one profile can serve
// the configured search. The composition layer uses it to skip unsupported
// routes at startup instead of creating a run whose first page always fails.
func (a *Adapter) SupportsSearch(profileID core.ProfileID, raw json.RawMessage) error {
	if err := a.ValidateSearch(raw); err != nil {
		return err
	}
	var query SearchQuery
	if err := json.Unmarshal(raw, &query); err != nil {
		return fmt.Errorf("decode hh search query: %w", err)
	}
	operation := "vacancies.search." + string(query.Source)
	a.mu.RLock()
	client := a.clients[profileID]
	browserClient := a.browserClients[profileID]
	a.mu.RUnlock()
	if client == nil && browserClient == nil {
		return operationError(core.ErrorUnauthorized, operation, "HH profile has no bound read session", nil)
	}
	switch query.Source {
	case SearchSourceGlobal:
		return nil
	case SearchSourceSimilarResume:
		if browserClient != nil || (client != nil && !query.hasModernWorkFields()) {
			return nil
		}
	case SearchSourceSimilarVacancy:
		if client != nil && !query.hasModernWorkFields() {
			return nil
		}
	case SearchSourceRelatedVacancy:
		if client != nil {
			return nil
		}
	}
	return hhUnsupported(operation)
}

func (a *Adapter) Search(ctx context.Context, profileID core.ProfileID, raw json.RawMessage, cursor string) (core.SearchPage, error) {
	if err := a.ValidateSearch(raw); err != nil {
		return core.SearchPage{}, err
	}
	var query SearchQuery
	if err := json.Unmarshal(raw, &query); err != nil {
		return core.SearchPage{}, fmt.Errorf("decode hh search query: %w", err)
	}
	operation := "vacancies.search." + string(query.Source)
	a.mu.RLock()
	client := a.clients[profileID]
	browserClient := a.browserClients[profileID]
	a.mu.RUnlock()
	switch query.Source {
	case SearchSourceGlobal:
		if client != nil {
			return client.SearchGlobal(ctx, query, cursor)
		}
		if browserClient != nil {
			return browserClient.SearchGlobal(ctx, query, cursor)
		}
	case SearchSourceSimilarResume:
		// The classic API endpoint predates the modern work fields. When they
		// are requested, prefer the browser search that supports the full web
		// filter set instead of silently dropping them.
		if client != nil && !query.hasModernWorkFields() {
			return client.SearchSimilarResume(ctx, query, cursor)
		}
		if browserClient != nil {
			return browserClient.SearchSimilarResume(ctx, query, cursor)
		}
	case SearchSourceSimilarVacancy:
		if client != nil && !query.hasModernWorkFields() {
			return client.SearchSimilarVacancy(ctx, query, cursor)
		}
	case SearchSourceRelatedVacancy:
		if client != nil {
			return client.SearchRelatedVacancy(ctx, query, cursor)
		}
	}
	if client == nil && browserClient == nil {
		return core.SearchPage{}, operationError(core.ErrorUnauthorized, operation, "HH profile has no bound read session", nil)
	}
	return core.SearchPage{}, hhUnsupported(operation)
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

func (a *Adapter) ReadProfileState(ctx context.Context, request adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
	a.mu.RLock()
	apiClient := a.clients[request.ProfileID]
	browserClient := a.browserClients[request.ProfileID]
	a.mu.RUnlock()
	if resumeProfilePaths(request.Paths) && apiClient != nil {
		return apiClient.ReadProfileState(ctx, request)
	}
	if browserClient != nil {
		return browserClient.ReadProfileState(ctx, request)
	}
	if apiClient != nil {
		return apiClient.ReadProfileState(ctx, request)
	}
	return core.ProfileStateObservation{}, operationError(core.ErrorUnauthorized, "profile_state.read", "HH profile has no compatible profile state session", nil)
}

func (a *Adapter) ApplyProfileState(ctx context.Context, proposal core.ProfileStateProposal) (adapter.ProfileStateApplyResult, error) {
	paths, err := proposal.DeclaredPaths()
	if err != nil {
		return adapter.ProfileStateApplyResult{}, err
	}
	a.mu.RLock()
	apiClient := a.clients[proposal.ProfileID]
	browserClient := a.browserProfileStateClients[proposal.ProfileID]
	a.mu.RUnlock()
	if resumeProfilePaths(paths) && apiClient != nil {
		return apiClient.ApplyProfileState(ctx, proposal)
	}
	if browserClient != nil {
		return browserClient.ApplyProfileState(ctx, proposal)
	}
	if apiClient != nil {
		return apiClient.ApplyProfileState(ctx, proposal)
	}
	return adapter.ProfileStateApplyResult{}, operationError(core.ErrorUnauthorized, "profile_state.apply", "HH profile has no bound profile state session", nil)
}

func resumeProfilePaths(paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	for _, path := range paths {
		if _, ok := parseResumeProfilePath(path); !ok {
			return false
		}
	}
	return true
}

func (a *Adapter) ListSuitableResumes(ctx context.Context, profileID core.ProfileID, key core.VacancyKey) ([]adapter.SuitableResume, error) {
	if key.Platform != Name {
		return nil, errors.New("HH suitable resume reader received another platform")
	}
	a.mu.RLock()
	client := a.clients[profileID]
	browserClient := a.browserApplicationClients[profileID]
	a.mu.RUnlock()
	if client != nil {
		return client.ListSuitableResumes(ctx, profileID, key)
	}
	if browserClient != nil {
		return browserClient.ListSuitableResumes(ctx, profileID, key)
	}
	return nil, operationError(core.ErrorUnauthorized, "vacancies.suitable_resumes", "HH profile has no bound application session", nil)
}

func (a *Adapter) SubmitApplication(ctx context.Context, command adapter.ApplicationSubmitCommand) (adapter.ApplicationSubmitResult, error) {
	a.mu.RLock()
	client := a.clients[command.ProfileID]
	browserClient := a.browserApplicationClients[command.ProfileID]
	a.mu.RUnlock()
	if client != nil {
		return client.SubmitApplication(ctx, command)
	}
	if browserClient != nil {
		return browserClient.SubmitApplication(ctx, command)
	}
	return adapter.ApplicationSubmitResult{}, operationError(core.ErrorUnauthorized, "applications.submit", "HH profile has no bound application session", nil)
}

func (a *Adapter) ReconcileApplication(ctx context.Context, command adapter.ApplicationReconcileCommand) (adapter.ApplicationReconcileResult, error) {
	a.mu.RLock()
	client := a.clients[command.ProfileID]
	browserClient := a.browserApplicationClients[command.ProfileID]
	a.mu.RUnlock()
	if client != nil {
		return client.ReconcileApplication(ctx, command)
	}
	if browserClient != nil {
		return browserClient.ReconcileApplication(ctx, command)
	}
	return adapter.ApplicationReconcileResult{}, operationError(core.ErrorUnauthorized, "applications.reconcile", "HH profile has no bound application session", nil)
}

func (a *Adapter) ObserveApplicationStates(ctx context.Context, profileID core.ProfileID) (adapter.ApplicationStateObservationResult, error) {
	a.mu.RLock()
	client := a.clients[profileID]
	a.mu.RUnlock()
	if client == nil {
		return adapter.ApplicationStateObservationResult{}, operationError(core.ErrorUnauthorized, "applications.observe", "HH profile has no bound API session", nil)
	}
	return client.ObserveApplicationStates(ctx, profileID)
}

func hhUnsupported(operation string) error {
	return &core.OperationError{
		Category: core.ErrorUnsupported, Operation: operation, Platform: Name,
		Message: "HH transport operation is not implemented",
	}
}
