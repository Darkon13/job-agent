package hh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/core"
)

const (
	searchPageSize       = 100
	searchMaximumDepth   = 2000
	maxSearchAPIResponse = 8 << 20
)

type vacancySearchResponse struct {
	Items   []vacancySearchItem `json:"items"`
	Found   int                 `json:"found"`
	Page    int                 `json:"page"`
	Pages   int                 `json:"pages"`
	PerPage int                 `json:"per_page"`
}

type vacancySearchItem struct {
	ID                  string          `json:"id"`
	Name                string          `json:"name"`
	AlternateURL        string          `json:"alternate_url"`
	PublishedAt         string          `json:"published_at"`
	Archived            bool            `json:"archived"`
	HasTest             bool            `json:"has_test"`
	ResponseLetter      bool            `json:"response_letter_required"`
	ResponseURL         string          `json:"response_url"`
	ApplyAlternateURL   string          `json:"apply_alternate_url"`
	Employer            *searchEmployer `json:"employer"`
	SalaryRange         json.RawMessage `json:"salary_range"`
	Salary              json.RawMessage `json:"salary"`
	ProfessionalRoles   json.RawMessage `json:"professional_roles"`
	EmploymentForm      json.RawMessage `json:"employment_form"`
	WorkFormat          json.RawMessage `json:"work_format"`
	WorkScheduleByDays  json.RawMessage `json:"work_schedule_by_days"`
	WorkingHours        json.RawMessage `json:"working_hours"`
	Experience          json.RawMessage `json:"experience"`
	Description         string          `json:"description"`
	KeySkills           []namedItem     `json:"key_skills"`
	Relations           []string        `json:"relations"`
	AllowMessages       bool            `json:"allow_messages"`
	ClosedForApplicants bool            `json:"closed_for_applicants"`
	NegotiationsURL     string          `json:"negotiations_url"`
	SuitableResumesURL  string          `json:"suitable_resumes_url"`
	Languages           json.RawMessage `json:"languages"`
	DriverLicenseTypes  json.RawMessage `json:"driver_license_types"`
	VacancyProperties   json.RawMessage `json:"vacancy_properties"`
	Test                json.RawMessage `json:"test"`
}

type searchEmployer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type namedItem struct {
	Name string `json:"name"`
}

func (client *ReadClient) SearchGlobal(ctx context.Context, query SearchQuery, cursor string) (core.SearchPage, error) {
	parameters := encodeGlobalSearchQuery(query)
	endpoint := strings.TrimRight(client.apiBaseURL, "/") + "/vacancies"
	return client.searchVacancies(ctx, "vacancies.search.global", endpoint, parameters, query, cursor)
}

func (client *ReadClient) SearchSimilarResume(ctx context.Context, query SearchQuery, cursor string) (core.SearchPage, error) {
	parameters := encodeClassicSearchQuery(query)
	endpoint := strings.TrimRight(client.apiBaseURL, "/") + "/resumes/" + url.PathEscape(strings.TrimSpace(query.Resume)) + "/similar_vacancies"
	return client.searchVacancies(ctx, "vacancies.search.similar_resume", endpoint, parameters, query, cursor)
}

func (client *ReadClient) SearchSimilarVacancy(ctx context.Context, query SearchQuery, cursor string) (core.SearchPage, error) {
	parameters := encodeClassicSearchQuery(query)
	endpoint := strings.TrimRight(client.apiBaseURL, "/") + "/vacancies/" + url.PathEscape(strings.TrimSpace(query.Vacancy)) + "/similar_vacancies"
	return client.searchVacancies(ctx, "vacancies.search.similar_vacancy", endpoint, parameters, query, cursor)
}

func (client *ReadClient) SearchRelatedVacancy(ctx context.Context, query SearchQuery, cursor string) (core.SearchPage, error) {
	endpoint := strings.TrimRight(client.apiBaseURL, "/") + "/vacancies/" + url.PathEscape(strings.TrimSpace(query.Vacancy)) + "/related_vacancies"
	return client.searchVacancies(ctx, "vacancies.search.related_vacancy", endpoint, make(url.Values), query, cursor)
}

func (client *ReadClient) searchVacancies(ctx context.Context, operation, endpoint string, parameters url.Values, query SearchQuery, cursor string) (core.SearchPage, error) {
	page, err := decodeSearchCursor(operation, cursor)
	if err != nil {
		return core.SearchPage{}, err
	}
	select {
	case <-ctx.Done():
		return core.SearchPage{}, ctx.Err()
	case <-client.profileLock:
	}
	defer func() { client.profileLock <- struct{}{} }()

	credentials, err := loadOAuthCredentials(client.credentialsRef)
	if err != nil {
		return core.SearchPage{}, err
	}
	pageSize := query.PageSize
	if pageSize == 0 {
		pageSize = searchPageSize
	}
	parameters.Set("page", strconv.Itoa(page))
	parameters.Set("per_page", strconv.Itoa(pageSize))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+parameters.Encode(), nil)
	if err != nil {
		return core.SearchPage{}, fmt.Errorf("create HH vacancy search request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
	request.Header.Set("HH-User-Agent", client.userAgent)

	httpClient := *client.httpClient
	if httpClient.CheckRedirect == nil {
		httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	}
	response, err := httpClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return core.SearchPage{}, ctxErr
		}
		return core.SearchPage{}, operationError(core.ErrorTemporaryFailure, operation, "HH vacancy search request failed", err)
	}
	defer response.Body.Close()
	if err := classifySearchResponse(operation, response); err != nil {
		return core.SearchPage{}, err
	}

	var result vacancySearchResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxSearchAPIResponse))
	if err := decoder.Decode(&result); err != nil {
		return core.SearchPage{}, operationError(core.ErrorTemporaryFailure, operation, "HH returned an invalid vacancy search response", err)
	}
	if result.Page != page || result.PerPage != pageSize || result.Pages < 0 || result.Found < 0 {
		return core.SearchPage{}, operationError(core.ErrorPermanentFailure, operation, "HH returned inconsistent vacancy pagination", nil)
	}

	observedAt := time.Now().UTC()
	vacancies := make([]core.Vacancy, 0, len(result.Items))
	seen := make(map[string]struct{}, len(result.Items))
	for index, item := range result.Items {
		vacancy, err := normalizeSearchItem(item, observedAt)
		if err != nil {
			return core.SearchPage{}, fmt.Errorf("normalize HH vacancy %d: %w", index, err)
		}
		if _, exists := seen[vacancy.ExternalID]; exists {
			continue
		}
		seen[vacancy.ExternalID] = struct{}{}
		vacancies = append(vacancies, vacancy)
	}
	done := len(result.Items) == 0 || page+1 >= result.Pages || (page+1)*result.PerPage >= searchMaximumDepth
	if query.MaxPages > 0 && page+1 >= query.MaxPages {
		done = true
	}
	nextCursor := ""
	if !done {
		nextCursor = strconv.Itoa(page + 1)
	}
	return core.SearchPage{Vacancies: vacancies, NextCursor: nextCursor, Done: done}, nil
}

func decodeSearchCursor(operation, cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	page, err := strconv.Atoi(cursor)
	if err != nil || page < 0 || page >= searchMaximumDepth/searchPageSize {
		return 0, operationError(core.ErrorPermanentFailure, operation, "invalid or exhausted HH search cursor", err)
	}
	return page, nil
}

func classifySearchResponse(operation string, response *http.Response) error {
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return nil
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		drain(response.Body)
		return operationError(core.ErrorUnauthorized, operation, "HH credentials are expired, revoked or invalid", nil)
	case response.StatusCode == http.StatusTooManyRequests:
		drain(response.Body)
		failure := operationError(core.ErrorRateLimited, operation, "HH vacancy search was rate limited", nil)
		failure.RetryAfter = retryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
		return failure
	case response.StatusCode >= 500:
		drain(response.Body)
		return operationError(core.ErrorTemporaryFailure, operation, fmt.Sprintf("HH returned status %d", response.StatusCode), nil)
	default:
		drain(response.Body)
		return operationError(core.ErrorPermanentFailure, operation, fmt.Sprintf("HH rejected vacancy search with status %d", response.StatusCode), nil)
	}
}

func normalizeSearchItem(item vacancySearchItem, observedAt time.Time) (core.Vacancy, error) {
	if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Name) == "" {
		return core.Vacancy{}, errors.New("vacancy requires id and name")
	}
	state := core.VacancyStateOpen
	if item.Archived {
		state = core.VacancyStateArchived
	}
	var employer string
	attributes := map[string]any{
		"has_test":                 item.HasTest,
		"response_letter_required": item.ResponseLetter,
	}
	if item.Employer != nil {
		employer = item.Employer.Name
		attributes["employer_id"] = item.Employer.ID
	}
	putRawJSON(attributes, "salary_range", item.SalaryRange)
	if _, exists := attributes["salary_range"]; !exists {
		putRawJSON(attributes, "salary_range", item.Salary)
	}
	putRawJSON(attributes, "professional_roles", item.ProfessionalRoles)
	putRawJSON(attributes, "employment_form", item.EmploymentForm)
	putRawJSON(attributes, "work_format", item.WorkFormat)
	putRawJSON(attributes, "work_schedule_by_days", item.WorkScheduleByDays)
	putRawJSON(attributes, "working_hours", item.WorkingHours)
	putRawJSON(attributes, "experience", item.Experience)
	putRawJSON(attributes, "languages", item.Languages)
	putRawJSON(attributes, "driver_license_types", item.DriverLicenseTypes)
	putRawJSON(attributes, "vacancy_properties", item.VacancyProperties)
	putRawJSON(attributes, "test", item.Test)
	if item.Description != "" {
		attributes["description"] = item.Description
	}
	if len(item.KeySkills) != 0 {
		skills := make([]string, 0, len(item.KeySkills))
		for _, skill := range item.KeySkills {
			if value := strings.TrimSpace(skill.Name); value != "" {
				skills = append(skills, value)
			}
		}
		if len(skills) != 0 {
			attributes["key_skills"] = skills
		}
	}
	if len(item.Relations) != 0 {
		attributes["relations"] = append([]string(nil), item.Relations...)
	}
	attributes["allow_messages"] = item.AllowMessages
	attributes["closed_for_applicants"] = item.ClosedForApplicants
	if item.NegotiationsURL != "" {
		attributes["negotiations_url"] = item.NegotiationsURL
	}
	if item.SuitableResumesURL != "" {
		attributes["suitable_resumes_url"] = item.SuitableResumesURL
	}
	if item.ResponseURL != "" {
		attributes["response_url"] = item.ResponseURL
	}
	if item.ApplyAlternateURL != "" {
		attributes["apply_alternate_url"] = item.ApplyAlternateURL
	}
	publishedAt, err := parseHHTime(item.PublishedAt)
	if err != nil {
		return core.Vacancy{}, fmt.Errorf("published_at: %w", err)
	}
	return core.Vacancy{
		Platform: Name, ExternalID: item.ID, URL: item.AlternateURL, Title: item.Name,
		Employer: employer, State: state, PublishedAt: publishedAt, ObservedAt: observedAt,
		Attributes: attributes,
	}, nil
}

func putRawJSON(attributes map[string]any, key string, raw json.RawMessage) {
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	var value any
	if json.Unmarshal(raw, &value) == nil {
		attributes[key] = value
	}
}

func parseHHTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05-0700"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			parsed = parsed.UTC()
			return &parsed, nil
		}
	}
	return nil, fmt.Errorf("invalid HH timestamp %q", value)
}

// encodeGlobalSearchQuery includes the newer employment and work-format
// fields documented only for the global search endpoint.
func encodeGlobalSearchQuery(query SearchQuery) url.Values {
	return encodeSearchQuery(query, true)
}

// encodeClassicSearchQuery targets the similar/related endpoints whose
// documented field set predates the newer work fields.
func encodeClassicSearchQuery(query SearchQuery) url.Values {
	return encodeSearchQuery(query, false)
}

func encodeSearchQuery(query SearchQuery, includeModernFields bool) url.Values {
	values := make(url.Values)
	setString(values, "text", query.Text)
	addStrings(values, "search_field", query.SearchField)
	addStrings(values, "experience", query.Experience)
	addStrings(values, "employment", query.Employment)
	addStrings(values, "schedule", query.Schedule)
	addStrings(values, "area", query.Area)
	addStrings(values, "metro", query.Metro)
	addStrings(values, "professional_role", query.ProfessionalRole)
	addStrings(values, "industry", query.Industry)
	addStrings(values, "employer_id", query.EmployerID)
	addStrings(values, "excluded_employer_id", query.ExcludedEmployerID)
	setString(values, "currency", query.Currency)
	setInt(values, "salary", query.Salary)
	addStrings(values, "label", query.Label)
	setTrue(values, "only_with_salary", query.OnlyWithSalary)
	setInt(values, "period", query.Period)
	setString(values, "date_from", query.DateFrom)
	setString(values, "date_to", query.DateTo)
	setFloat(values, "top_lat", query.TopLat)
	setFloat(values, "bottom_lat", query.BottomLat)
	setFloat(values, "left_lng", query.LeftLng)
	setFloat(values, "right_lng", query.RightLng)
	setString(values, "order_by", query.OrderBy)
	setFloat(values, "sort_point_lat", query.SortPointLat)
	setFloat(values, "sort_point_lng", query.SortPointLng)
	setTrue(values, "clusters", query.Clusters)
	setTrue(values, "describe_arguments", query.DescribeArguments)
	setTrue(values, "no_magic", query.NoMagic)
	setTrue(values, "premium", query.Premium)
	setTrue(values, "responses_count_enabled", query.ResponsesCount)
	addStrings(values, "part_time", query.PartTime)
	setBool(values, "accept_temporary", query.AcceptTemporary)
	setString(values, "excluded_text", query.ExcludedText)
	addStrings(values, "education", query.Education)
	if includeModernFields {
		addStrings(values, "employment_form", query.EmploymentForm)
		addStrings(values, "work_schedule_by_days", query.WorkScheduleByDays)
		addStrings(values, "working_hours", query.WorkingHours)
		addStrings(values, "work_format", query.WorkFormat)
	}
	return values
}

func setString(values url.Values, key, value string) {
	if value != "" {
		values.Set(key, value)
	}
}

func addStrings(values url.Values, key string, entries []string) {
	for _, value := range entries {
		values.Add(key, value)
	}
}

func setInt(values url.Values, key string, value *int) {
	if value != nil {
		values.Set(key, strconv.Itoa(*value))
	}
}

func setFloat(values url.Values, key string, value *float64) {
	if value != nil {
		values.Set(key, strconv.FormatFloat(*value, 'f', -1, 64))
	}
}

func setTrue(values url.Values, key string, value bool) {
	if value {
		values.Set(key, "true")
	}
}

func setBool(values url.Values, key string, value *bool) {
	if value != nil {
		values.Set(key, strconv.FormatBool(*value))
	}
}
