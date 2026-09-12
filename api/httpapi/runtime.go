package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/Darkon13/job-agent/buildinfo"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

type RuntimeReadRepository interface {
	storage.ApplicationQueryRepository
	Stats(context.Context) (storage.RuntimeStats, error)
	TaskCounts(context.Context) ([]storage.TaskCount, error)
	ApplicationCounts(context.Context) ([]storage.ApplicationCount, error)
	ApplicationByID(context.Context, core.ApplicationID) (core.Application, error)
	ListApplications(context.Context, storage.ApplicationFilter) ([]core.Application, error)
	Vacancy(context.Context, core.VacancyKey) (core.Vacancy, error)
	ListApplicationCampaigns(context.Context, int) ([]core.ApplicationCampaign, error)
	ListCampaignApplicationStates(context.Context, core.ApplicationCampaignID) ([]core.CampaignApplicationState, error)
	ProfileActivityCounts(context.Context, storage.ProfileActivityFilter) ([]storage.ProfileActivityCount, error)
	ListProfileActivitySnapshots(context.Context, storage.ProfileActivitySnapshotFilter) ([]core.ProfileActivitySnapshot, error)
	ListConversations(context.Context, storage.ConversationFilter) ([]core.Conversation, error)
	OpenQuestionnaireConversationIDs(context.Context) ([]core.ConversationID, error)
}

type RuntimeAPI struct {
	repository RuntimeReadRepository
	profiles   []core.ProfileID
	now        func() time.Time
}

type DashboardSummary struct {
	GeneratedAt       time.Time                      `json:"generated_at"`
	Profiles          []core.ProfileID               `json:"profiles,omitempty"`
	Stats             storage.RuntimeStats           `json:"stats"`
	Tasks             []storage.TaskCount            `json:"tasks"`
	Applications      []storage.ApplicationCount     `json:"applications"`
	Campaigns         []ApplicationCampaignSummary   `json:"campaigns"`
	Activity          []storage.ProfileActivityCount `json:"activity"`
	ActivitySnapshots []core.ProfileActivitySnapshot `json:"activity_snapshots"`
	Conversations     []ConversationSummary          `json:"conversations"`
}

type ApplicationCampaignSummary struct {
	ID               core.ApplicationCampaignID     `json:"id"`
	JobTag           string                         `json:"job_tag"`
	Status           core.ApplicationCampaignStatus `json:"status"`
	StopReason       string                         `json:"stop_reason,omitempty"`
	TargetSuccessful int                            `json:"target_successful"`
	RouteIndex       int                            `json:"route_index"`
	RouteCount       int                            `json:"route_count"`
	Applications     []storage.ApplicationCount     `json:"applications"`
	CreatedAt        time.Time                      `json:"created_at"`
	UpdatedAt        time.Time                      `json:"updated_at"`
}

type ConversationSummary struct {
	ID                core.ConversationID     `json:"id"`
	Platform          core.Platform           `json:"platform"`
	ProfileID         core.ProfileID          `json:"profile_id"`
	Status            core.ConversationStatus `json:"status"`
	LastMessageAt     *time.Time              `json:"last_message_at,omitempty"`
	LastIncomingAt    *time.Time              `json:"last_incoming_at,omitempty"`
	UpdatedAt         time.Time               `json:"updated_at"`
	Revision          uint64                  `json:"revision"`
	VacancyTitle      string                  `json:"vacancy_title,omitempty"`
	Employer          string                  `json:"employer,omitempty"`
	VacancyURL        string                  `json:"vacancy_url,omitempty"`
	QuestionnaireOpen bool                    `json:"questionnaire_open,omitempty"`
	UnreadCount       int                     `json:"unread_count"`
}

// ApplicationSummary is an operator-facing object. It intentionally omits
// prepared messages, decision reasons and external negotiation identifiers.
type ApplicationSummary struct {
	ID                 core.ApplicationID           `json:"id"`
	Platform           core.Platform                `json:"platform"`
	ProfileID          core.ProfileID               `json:"profile_id"`
	Status             core.ApplicationStatus       `json:"status"`
	DecisionCode       string                       `json:"decision_code,omitempty"`
	FailureCategory    core.ErrorCategory           `json:"failure_category,omitempty"`
	Attempts           int                          `json:"attempts"`
	VacancyTitle       string                       `json:"vacancy_title"`
	Employer           string                       `json:"employer,omitempty"`
	VacancyURL         string                       `json:"vacancy_url,omitempty"`
	HasCoverLetter     bool                         `json:"has_cover_letter"`
	UpdatedAt          time.Time                    `json:"updated_at"`
	SubmittedAt        *time.Time                   `json:"submitted_at,omitempty"`
	PlatformState      string                       `json:"platform_state,omitempty"`
	Disposition        core.ApplicationDisposition  `json:"disposition,omitempty"`
	ViewedByOpponent   *bool                        `json:"viewed_by_opponent,omitempty"`
	PlatformObservedAt *time.Time                   `json:"platform_observed_at,omitempty"`
	Tailoring          *ApplicationTailoringSummary `json:"tailoring,omitempty"`
}

// ApplicationTailoringSummary exposes the active temporary resume mutation
// without leaking snapshot contents. Only skill array entries become values.
type ApplicationTailoringSummary struct {
	Status           core.ApplicationTailoringStatus   `json:"status"`
	ProcessorTag     string                            `json:"processor_tag"`
	ProcessorVersion string                            `json:"processor_version"`
	RecoveryReason   string                            `json:"recovery_reason,omitempty"`
	Changes          []core.ApplicationTailoringChange `json:"changes,omitempty"`
	UpdatedAt        time.Time                         `json:"updated_at"`
}

func NewRuntimeAPI(repository RuntimeReadRepository, profiles []core.ProfileID) (*RuntimeAPI, error) {
	if repository == nil {
		return nil, errors.New("runtime API requires repository")
	}
	return &RuntimeAPI{repository: repository, profiles: append([]core.ProfileID(nil), profiles...), now: time.Now}, nil
}

// Handler adds health and dashboard routes in front of the supplied product
// API. The dashboard remains an API client; it never receives direct storage
// or workflow access.
func (api *RuntimeAPI) Handler(productAPI http.Handler) http.Handler {
	if productAPI == nil {
		productAPI = http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", api.health)
	mux.HandleFunc("GET /readyz", api.ready)
	mux.HandleFunc("GET /api/v1/version", api.version)
	mux.HandleFunc("GET /api/v1/dashboard/summary", api.summary)
	mux.HandleFunc("GET /api/v1/applications", api.listApplications)
	mux.Handle("/", productAPI)
	return mux
}

func (api *RuntimeAPI) version(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, buildinfo.Current())
}

func (api *RuntimeAPI) health(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, struct {
		Status string `json:"status"`
	}{Status: "ok"})
}

func (api *RuntimeAPI) ready(response http.ResponseWriter, request *http.Request) {
	if _, err := api.repository.Stats(request.Context()); err != nil {
		writeProblem(response, http.StatusServiceUnavailable, "storage is not ready")
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Status string `json:"status"`
	}{Status: "ready"})
}

func (api *RuntimeAPI) summary(response http.ResponseWriter, request *http.Request) {
	stats, err := api.repository.Stats(request.Context())
	if err != nil {
		writeProblem(response, http.StatusInternalServerError, "load runtime stats")
		return
	}
	tasks, err := api.repository.TaskCounts(request.Context())
	if err != nil {
		writeProblem(response, http.StatusInternalServerError, "load task summary")
		return
	}
	applications, err := api.repository.ApplicationCounts(request.Context())
	if err != nil {
		writeProblem(response, http.StatusInternalServerError, "load application summary")
		return
	}
	campaigns, err := api.repository.ListApplicationCampaigns(request.Context(), 20)
	if err != nil {
		writeProblem(response, http.StatusInternalServerError, "load application campaigns")
		return
	}
	campaignSummaries := make([]ApplicationCampaignSummary, 0, len(campaigns))
	for _, campaign := range campaigns {
		states, err := api.repository.ListCampaignApplicationStates(request.Context(), campaign.ID)
		if err != nil {
			writeProblem(response, http.StatusInternalServerError, "load application campaign outcomes")
			return
		}
		campaignSummaries = append(campaignSummaries, applicationCampaignSummary(campaign, states))
	}
	activity, err := api.repository.ProfileActivityCounts(request.Context(), storage.ProfileActivityFilter{})
	if err != nil {
		writeProblem(response, http.StatusInternalServerError, "load profile activity summary")
		return
	}
	activitySnapshots, err := api.repository.ListProfileActivitySnapshots(request.Context(), storage.ProfileActivitySnapshotFilter{Limit: 50})
	if err != nil {
		writeProblem(response, http.StatusInternalServerError, "load profile activity observations")
		return
	}
	conversations, err := api.repository.ListConversations(request.Context(), storage.ConversationFilter{})
	if err != nil {
		writeProblem(response, http.StatusInternalServerError, "load conversations")
		return
	}
	openQuestionnaires, err := api.repository.OpenQuestionnaireConversationIDs(request.Context())
	if err != nil {
		writeProblem(response, http.StatusInternalServerError, "load open questionnaires")
		return
	}
	openSet := make(map[core.ConversationID]struct{}, len(openQuestionnaires))
	for _, id := range openQuestionnaires {
		openSet[id] = struct{}{}
	}
	conversationSummaries := make([]ConversationSummary, 0, len(conversations))
	for _, conversation := range conversations {
		_, questionnaireOpen := openSet[conversation.ID]
		summary := ConversationSummary{
			QuestionnaireOpen: questionnaireOpen,
			ID:                conversation.ID, Platform: conversation.Platform, ProfileID: conversation.ProfileID,
			Status: conversation.Status, LastMessageAt: conversation.LastMessageAt,
			LastIncomingAt: conversation.LastIncomingAt, UpdatedAt: conversation.UpdatedAt,
			Revision: conversation.Revision, VacancyTitle: conversation.VacancyTitle,
			Employer: conversation.Employer, VacancyURL: conversation.VacancyURL,
			UnreadCount: conversation.UnreadCount,
		}
		if summary.VacancyTitle == "" && conversation.ApplicationID != "" {
			if application, err := api.repository.ApplicationByID(request.Context(), conversation.ApplicationID); err == nil {
				if vacancy, err := api.repository.Vacancy(request.Context(), application.Key.Vacancy); err == nil {
					summary.VacancyTitle = vacancy.Title
					summary.Employer = vacancy.Employer
					summary.VacancyURL = vacancy.URL
				}
			}
		}
		conversationSummaries = append(conversationSummaries, summary)
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, DashboardSummary{
		GeneratedAt:       api.now().UTC(),
		Profiles:          api.profiles,
		Stats:             stats,
		Tasks:             tasks,
		Applications:      applications,
		Campaigns:         campaignSummaries,
		Activity:          activity,
		ActivitySnapshots: activitySnapshots,
		Conversations:     conversationSummaries,
	})
}

func applicationCampaignSummary(campaign core.ApplicationCampaign, states []core.CampaignApplicationState) ApplicationCampaignSummary {
	type countKey struct {
		status       core.ApplicationStatus
		decisionCode string
	}
	counts := make(map[countKey]int)
	for _, state := range states {
		key := countKey{status: state.Application.Status, decisionCode: state.Application.DecisionCode}
		counts[key]++
	}
	applications := make([]storage.ApplicationCount, 0, len(counts))
	for key, count := range counts {
		applications = append(applications, storage.ApplicationCount{
			Status: key.status, DecisionCode: key.decisionCode, Count: count,
		})
	}
	sort.Slice(applications, func(i, j int) bool {
		if applications[i].Status != applications[j].Status {
			return applications[i].Status < applications[j].Status
		}
		return applications[i].DecisionCode < applications[j].DecisionCode
	})
	return ApplicationCampaignSummary{
		ID: campaign.ID, JobTag: campaign.JobTag, Status: campaign.Status, StopReason: campaign.StopReason,
		TargetSuccessful: campaign.TargetSuccessful, RouteIndex: campaign.RouteIndex, RouteCount: len(campaign.Routes),
		Applications: applications, CreatedAt: campaign.CreatedAt, UpdatedAt: campaign.UpdatedAt,
	}
}
