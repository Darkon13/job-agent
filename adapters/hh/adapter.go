package hh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

const Name = "hh"

var ErrNotImplemented = errors.New("hh transport is not implemented")

type Config struct {
	APIDelayMS int `json:"api_delay_ms,omitempty"`
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
}

type Adapter struct {
	config Config
}

var _ adapter.Adapter = (*Adapter)(nil)
var _ adapter.ConversationTransport = (*Adapter)(nil)

func New(raw json.RawMessage) (adapter.Adapter, error) {
	var cfg Config
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("decode hh config: %w", err)
		}
	}
	return &Adapter{config: cfg}, nil
}

func (a *Adapter) Name() string { return Name }

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
	if err := json.Unmarshal(raw, &query); err != nil {
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
	if query.Period != nil && *query.Period <= 0 {
		return errors.New("hh period must be positive")
	}
	if query.Salary != nil && *query.Salary < 0 {
		return errors.New("hh salary must not be negative")
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
	return nil
}

func (a *Adapter) Search(context.Context, core.ProfileID, json.RawMessage, string) (core.SearchPage, error) {
	return core.SearchPage{}, ErrNotImplemented
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
		Message: "HH conversation transport is not implemented",
	}
}
