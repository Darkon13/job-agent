package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	CountConversations(context.Context, storage.ConversationFilter) (storage.ConversationCounts, error)
	OpenQuestionnaireConversationIDs(context.Context) ([]core.ConversationID, error)
}

// ProfileSummary is the dashboard-facing profile identity: configuration tag
// plus the resolved sender name, never credentials.
type ProfileSummary struct {
	ID          core.ProfileID `json:"id"`
	DisplayName string         `json:"display_name,omitempty"`
}

// QuestionnaireCapturer enqueues the read-only capture of a vacancy
// questionnaire so the dashboard can answer it through review sessions.
type QuestionnaireCapturer interface {
	EnqueueCapture(ctx context.Context, profileID core.ProfileID, platform core.Platform, externalID string, source string) (bool, error)
}

type RuntimeAPI struct {
	repository     RuntimeReadRepository
	profiles       []ProfileSummary
	now            func() time.Time
	questionnaires QuestionnaireCapturer
	configStatus   func() ConfigStatus

	summaryMu      sync.Mutex
	summaryCache   []byte
	summaryExpires time.Time
	summaryTTL     time.Duration
}

// ConfigureSummaryCache serves a recently generated summary for the given
// window. The dashboard refreshes on every change notification, so a short
// window collapses bursts of identical requests.
func (api *RuntimeAPI) ConfigureSummaryCache(ttl time.Duration) {
	if ttl < 0 {
		ttl = 0
	}
	api.summaryTTL = ttl
}

// ConfigureConfigStatus attaches the live reload status shown in the summary.
func (api *RuntimeAPI) ConfigureConfigStatus(provider func() ConfigStatus) {
	api.configStatus = provider
}

// ConfigureQuestionnaireCapture attaches the capture enqueue. Without it the
// questionnaire endpoint reports that capture is unavailable.
func (api *RuntimeAPI) ConfigureQuestionnaireCapture(capturer QuestionnaireCapturer) {
	api.questionnaires = capturer
}

// ConfigStatus reports the last applied configuration reload.
type ConfigStatus struct {
	Digest      string    `json:"digest,omitempty"`
	AppliedAt   time.Time `json:"applied_at,omitempty"`
	Definitions int       `json:"definitions,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
}

type DashboardSummary struct {
	Config            ConfigStatus                   `json:"config_status"`
	GeneratedAt       time.Time                      `json:"generated_at"`
	Profiles          []ProfileSummary               `json:"profiles,omitempty"`
	Stats             storage.RuntimeStats           `json:"stats"`
	Tasks             []storage.TaskCount            `json:"tasks"`
	Applications      []storage.ApplicationCount     `json:"applications"`
	Campaigns         []ApplicationCampaignSummary   `json:"campaigns"`
	Activity          []storage.ProfileActivityCount `json:"activity"`
	ActivitySnapshots []core.ProfileActivitySnapshot `json:"activity_snapshots"`
	ConversationStats storage.ConversationCounts     `json:"conversation_stats"`
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

func NewRuntimeAPI(repository RuntimeReadRepository, profiles []ProfileSummary) (*RuntimeAPI, error) {
	if repository == nil {
		return nil, errors.New("runtime API requires repository")
	}
	return &RuntimeAPI{repository: repository, profiles: append([]ProfileSummary(nil), profiles...), now: time.Now}, nil
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
	mux.HandleFunc("GET /api/v1/conversations", api.listConversations)
	mux.HandleFunc("GET /api/v1/events", api.events)
	mux.HandleFunc("POST /api/v1/applications/{application_id}/questionnaire", api.captureQuestionnaire)
	mux.Handle("/", productAPI)
	return mux
}

// events streams lightweight change notifications so the dashboard does not
// depend on the 30 second poll. The conversation revision replaces the full
// payload: a new event simply tells the client to refresh.
func (api *RuntimeAPI) events(response http.ResponseWriter, request *http.Request) {
	flusher, ok := response.(http.Flusher)
	if !ok {
		writeProblem(response, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Connection", "keep-alive")
	generation := 0
	last := map[string]string{}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case <-heartbeat.C:
			_, _ = io.WriteString(response, ": ping\n\n")
			flusher.Flush()
		case <-ticker.C:
			sections, err := api.changedSections(request.Context(), last)
			if err != nil {
				return
			}
			if len(sections) == 0 {
				continue
			}
			generation++
			payload, err := json.Marshal(struct {
				Generation int      `json:"generation"`
				Sections   []string `json:"sections"`
			}{Generation: generation, Sections: sections})
			if err != nil {
				return
			}
			fmt.Fprintf(response, "event: dashboard\ndata: %s\n\n", payload)
			flusher.Flush()
		}
	}
}

// changedSections compares per-section fingerprints and returns the sections
// that actually changed, so the dashboard refreshes only what moved instead of
// rebuilding every panel on each event.
func (api *RuntimeAPI) changedSections(ctx context.Context, last map[string]string) ([]string, error) {
	current := make(map[string]string, 3)
	conversations, err := api.repository.ListConversations(ctx, storage.ConversationFilter{})
	if err != nil {
		return nil, err
	}
	conversationDigest := sha256.New()
	for _, conversation := range conversations {
		fmt.Fprintf(conversationDigest, "%s\x00%d\x00%s\x00%d\n", conversation.ID, conversation.Revision, conversation.LastMessageID, conversation.UpdatedAt.UnixNano())
	}
	current["conversations"] = hex.EncodeToString(conversationDigest.Sum(nil))

	taskCounts, err := api.repository.TaskCounts(ctx)
	if err != nil {
		return nil, err
	}
	taskDigest := sha256.New()
	for _, count := range taskCounts {
		fmt.Fprintf(taskDigest, "%s\x00%s\x00%d\n", count.Type, count.Status, count.Count)
	}
	current["tasks"] = hex.EncodeToString(taskDigest.Sum(nil))

	applicationCounts, err := api.repository.ApplicationCounts(ctx)
	if err != nil {
		return nil, err
	}
	applicationDigest := sha256.New()
	for _, count := range applicationCounts {
		fmt.Fprintf(applicationDigest, "%s\x00%s\x00%d\n", count.Status, count.DecisionCode, count.Count)
	}
	current["applications"] = hex.EncodeToString(applicationDigest.Sum(nil))

	sections := make([]string, 0, 3)
	for _, name := range []string{"conversations", "tasks", "applications"} {
		if last[name] == current[name] {
			continue
		}
		last[name] = current[name]
		sections = append(sections, name)
	}
	return sections, nil
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
	if payload, ok := api.cachedSummary(); ok {
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "no-store")
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write(payload)
		return
	}
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
	conversationCounts, err := api.repository.CountConversations(request.Context(), storage.ConversationFilter{})
	if err != nil {
		writeProblem(response, http.StatusInternalServerError, "load conversation counters")
		return
	}
	configStatus := ConfigStatus{}
	if api.configStatus != nil {
		configStatus = api.configStatus()
	}
	payload, err := json.Marshal(DashboardSummary{
		Config:            configStatus,
		GeneratedAt:       api.now().UTC(),
		Profiles:          api.profiles,
		Stats:             stats,
		Tasks:             tasks,
		Applications:      applications,
		Campaigns:         campaignSummaries,
		Activity:          activity,
		ActivitySnapshots: activitySnapshots,
		ConversationStats: conversationCounts,
	})
	if err != nil {
		writeProblem(response, http.StatusInternalServerError, "encode summary")
		return
	}
	api.storeSummary(payload)
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(payload)
}

// cachedSummary returns a recently generated payload inside the cache window.
func (api *RuntimeAPI) cachedSummary() ([]byte, bool) {
	if api.summaryTTL <= 0 {
		return nil, false
	}
	api.summaryMu.Lock()
	defer api.summaryMu.Unlock()
	if len(api.summaryCache) == 0 || !time.Now().Before(api.summaryExpires) {
		return nil, false
	}
	return append([]byte(nil), api.summaryCache...), true
}

func (api *RuntimeAPI) storeSummary(payload []byte) {
	if api.summaryTTL <= 0 {
		return
	}
	api.summaryMu.Lock()
	defer api.summaryMu.Unlock()
	api.summaryCache = append([]byte(nil), payload...)
	api.summaryExpires = time.Now().Add(api.summaryTTL)
}

// listConversations is the paginated dashboard feed: the summary carries only
// counters, while the operator scrolls the list page by page.
func (api *RuntimeAPI) listConversations(response http.ResponseWriter, request *http.Request) {
	filter := storage.ConversationFilter{
		ProfileID: core.ProfileID(strings.TrimSpace(request.URL.Query().Get("profile_id"))),
		Platform:  core.Platform(strings.TrimSpace(request.URL.Query().Get("platform"))),
		Status:    core.ConversationStatus(strings.TrimSpace(request.URL.Query().Get("status"))),
		Query:     strings.TrimSpace(request.URL.Query().Get("q")),
		Limit:     50,
	}
	if value := strings.TrimSpace(request.URL.Query().Get("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			writeProblem(response, http.StatusBadRequest, "conversation limit must be between 1 and 200")
			return
		}
		filter.Limit = parsed
	}
	if value := strings.TrimSpace(request.URL.Query().Get("offset")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			writeProblem(response, http.StatusBadRequest, "invalid conversation offset")
			return
		}
		filter.Offset = parsed
	}
	conversations, err := api.repository.ListConversations(request.Context(), filter)
	if err != nil {
		writeProblem(response, http.StatusInternalServerError, "load conversations")
		return
	}
	counts, err := api.repository.CountConversations(request.Context(), filter)
	if err != nil {
		writeProblem(response, http.StatusInternalServerError, "load conversation counters")
		return
	}
	items, err := api.conversationSummaries(request.Context(), conversations)
	if err != nil {
		writeProblem(response, http.StatusInternalServerError, "build conversation summaries")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, struct {
		Items       []ConversationSummary `json:"items"`
		Total       int                   `json:"total"`
		Offset      int                   `json:"offset"`
		Limit       int                   `json:"limit"`
		UnreadTotal int                   `json:"unread_total"`
		ActiveTotal int                   `json:"active_total"`
	}{Items: items, Total: counts.Total, Offset: filter.Offset, Limit: filter.Limit, UnreadTotal: counts.Unread, ActiveTotal: counts.Active})
}

// conversationSummaries decorates stored conversations with the open
// questionnaire flag and the vacancy fallback used by the dashboard.
func (api *RuntimeAPI) conversationSummaries(ctx context.Context, conversations []core.Conversation) ([]ConversationSummary, error) {
	if len(conversations) == 0 {
		return []ConversationSummary{}, nil
	}
	openQuestionnaires, err := api.repository.OpenQuestionnaireConversationIDs(ctx)
	if err != nil {
		return nil, err
	}
	openSet := make(map[core.ConversationID]struct{}, len(openQuestionnaires))
	for _, id := range openQuestionnaires {
		openSet[id] = struct{}{}
	}
	result := make([]ConversationSummary, 0, len(conversations))
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
			if application, err := api.repository.ApplicationByID(ctx, conversation.ApplicationID); err == nil {
				if vacancy, err := api.repository.Vacancy(ctx, application.Key.Vacancy); err == nil {
					summary.VacancyTitle = vacancy.Title
					summary.Employer = vacancy.Employer
					summary.VacancyURL = vacancy.URL
				}
			}
		}
		result = append(result, summary)
	}
	return result, nil
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
