package hh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

const (
	defaultWebBaseURL      = "https://hh.ru"
	maxBrowserHTMLResponse = 16 << 20
)

// BrowserReadClient replays an exported Playwright storage state only for
// idempotent GET requests. It implements no submit or conversation transport.
type BrowserReadClient struct {
	profileID  core.ProfileID
	stateFile  string
	webBaseURL string
	userAgent  string
	httpClient *http.Client
	mu         sync.Mutex
}

var _ adapter.VacancyReader = (*BrowserReadClient)(nil)
var _ adapter.ProfileStateReader = (*BrowserReadClient)(nil)

func NewBrowserReadClient(profileID core.ProfileID, stateFile, userAgent string, client *http.Client) (*BrowserReadClient, error) {
	if profileID == "" {
		return nil, errors.New("HH browser reader requires profile")
	}
	if strings.TrimSpace(stateFile) == "" {
		return nil, errors.New("HH browser reader requires state file")
	}
	if strings.TrimSpace(userAgent) == "" {
		userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &BrowserReadClient{
		profileID: profileID, stateFile: stateFile, webBaseURL: defaultWebBaseURL,
		userAgent: userAgent, httpClient: client,
	}, nil
}

func (client *BrowserReadClient) SearchGlobal(ctx context.Context, query SearchQuery, cursor string) (core.SearchPage, error) {
	return client.searchWeb(ctx, query, cursor, "")
}

func (client *BrowserReadClient) SearchSimilarResume(ctx context.Context, query SearchQuery, cursor string) (core.SearchPage, error) {
	return client.searchWeb(ctx, query, cursor, strings.TrimSpace(query.Resume))
}

func (client *BrowserReadClient) searchWeb(ctx context.Context, query SearchQuery, cursor, resume string) (core.SearchPage, error) {
	pageNumber, err := decodeSearchCursor("vacancies.search.browser", cursor)
	if err != nil {
		return core.SearchPage{}, err
	}
	pageSize := query.PageSize
	if pageSize == 0 {
		pageSize = 20
	}
	maxPages := query.MaxPages
	if maxPages == 0 {
		maxPages = 1
	}
	if pageNumber >= maxPages {
		return core.SearchPage{Done: true}, nil
	}

	parameters := encodeGlobalSearchQuery(query)
	if resume != "" {
		parameters.Set("resume", resume)
	}
	parameters.Set("page", fmt.Sprint(pageNumber))
	parameters.Set("items_on_page", fmt.Sprint(pageSize))
	endpoint := strings.TrimRight(client.webBaseURL, "/") + "/search/vacancy?" + parameters.Encode()
	document, finalURL, err := client.getHTML(ctx, endpoint, "vacancies.search.browser")
	if err != nil {
		return core.SearchPage{}, err
	}
	if isLoginURL(finalURL) {
		return core.SearchPage{}, operationError(core.ErrorUnauthorized, "vacancies.search.browser", "HH browser session requires authentication", nil)
	}

	observedAt := time.Now().UTC()
	vacancies := make([]core.Vacancy, 0, pageSize)
	seen := make(map[string]struct{}, pageSize)
	walkHTML(document, func(node *html.Node) bool {
		if len(vacancies) >= pageSize || htmlAttribute(node, "data-qa") != "vacancy-serp__vacancy" {
			return true
		}
		titleNode := findHTMLNode(node, func(candidate *html.Node) bool {
			return candidate.Type == html.ElementNode && candidate.Data == "a" && htmlAttribute(candidate, "data-qa") == "serp-item__title"
		})
		if titleNode == nil {
			return true
		}
		href := htmlAttribute(titleNode, "href")
		externalID := vacancyIDFromURL(href)
		title := htmlText(titleNode)
		if externalID == "" || title == "" {
			return true
		}
		if _, exists := seen[externalID]; exists {
			return true
		}
		seen[externalID] = struct{}{}
		employerNode := findHTMLNode(node, func(candidate *html.Node) bool {
			return htmlAttribute(candidate, "data-qa") == "vacancy-serp__vacancy-employer"
		})
		vacancyURL, _ := url.Parse(href)
		if vacancyURL != nil {
			baseURL, _ := url.Parse(client.webBaseURL)
			vacancyURL = baseURL.ResolveReference(vacancyURL)
			vacancyURL.RawQuery = ""
			vacancyURL.Fragment = ""
			href = vacancyURL.String()
		}
		vacancies = append(vacancies, core.Vacancy{
			Platform: Name, ExternalID: externalID, URL: href, Title: title,
			Employer: htmlText(employerNode), State: core.VacancyStateOpen, ObservedAt: observedAt,
			Attributes: map[string]any{"read_channel": "browser"},
		})
		return true
	})

	done := pageNumber+1 >= maxPages || len(vacancies) < pageSize
	nextCursor := ""
	if !done {
		nextCursor = fmt.Sprint(pageNumber + 1)
	}
	return core.SearchPage{Vacancies: vacancies, NextCursor: nextCursor, Done: done}, nil
}

func (client *BrowserReadClient) ReadVacancy(ctx context.Context, profileID core.ProfileID, key core.VacancyKey) (core.Vacancy, error) {
	if profileID == "" || profileID != client.profileID {
		return core.Vacancy{}, errors.New("HH browser vacancy reader profile does not match")
	}
	if err := key.Validate(); err != nil {
		return core.Vacancy{}, err
	}
	if key.Platform != Name {
		return core.Vacancy{}, errors.New("HH browser vacancy reader requires an HH vacancy")
	}
	endpoint := strings.TrimRight(client.webBaseURL, "/") + "/vacancy/" + url.PathEscape(key.ExternalID)
	document, finalURL, err := client.getHTML(ctx, endpoint, "vacancies.read.browser")
	if err != nil {
		if isBrowserResourceUnavailable(err) {
			// HH answers 403 for a vacancy the account may not see. Retrying
			// cannot change that, so the application is skipped with a reason.
			return core.Vacancy{}, &core.OperationError{
				Category: core.ErrorPermanentFailure, Operation: "vacancies.read.browser", Platform: Name,
				Message: "HH vacancy is closed or unavailable", Cause: err,
				Metadata: map[string]string{"code": "vacancy_closed"},
			}
		}
		return core.Vacancy{}, err
	}
	if isLoginURL(finalURL) {
		return core.Vacancy{}, operationError(core.ErrorUnauthorized, "vacancies.read.browser", "HH browser session requires authentication", nil)
	}
	title := htmlText(findHTMLByQA(document, "vacancy-title"))
	if title == "" {
		if browserVacancyPageLooksClosed(document) {
			closed := &core.OperationError{
				Category: core.ErrorValidationRequired, Operation: "vacancies.read.browser", Platform: Name,
				Message:  "HH vacancy is closed or unavailable",
				Metadata: map[string]string{"code": "vacancy_closed"},
			}
			return core.Vacancy{}, closed
		}
		return core.Vacancy{}, operationError(core.ErrorPermanentFailure, "vacancies.read.browser", "HH vacancy page has no vacancy title", nil)
	}
	employer := htmlText(findHTMLByQA(document, "vacancy-company-name"))
	description := htmlText(findHTMLByQA(document, "vacancy-description"))
	skills := make([]string, 0)
	walkHTML(document, func(node *html.Node) bool {
		if htmlAttribute(node, "data-qa") == "skills-element" {
			if skill := htmlText(node); skill != "" {
				skills = append(skills, skill)
			}
		}
		return true
	})
	state := core.VacancyStateOpen
	if findHTMLNode(document, func(node *html.Node) bool {
		value := htmlAttribute(node, "data-qa")
		return value == "vacancy-archive" || value == "vacancy-closed"
	}) != nil {
		state = core.VacancyStateArchived
	}
	attributes := map[string]any{"read_channel": "browser"}
	if description != "" {
		attributes["description"] = description
	}
	if len(skills) != 0 {
		attributes["key_skills"] = skills
	}
	return core.Vacancy{
		Platform: Name, ExternalID: key.ExternalID, URL: endpoint, Title: title,
		Employer: employer, State: state, ObservedAt: time.Now().UTC(), Attributes: attributes,
	}, nil
}

func (client *BrowserReadClient) ReadProfileState(ctx context.Context, request adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
	const operation = "profile_state.read.browser"
	if request.ProfileID == "" || request.ProfileID != client.profileID {
		return core.ProfileStateObservation{}, errors.New("HH browser profile state reader profile does not match")
	}
	aboutIDs := make(map[string]struct{}, len(request.Paths))
	webPaths := make(map[string][]browserResumePath)
	profilePaths := make(map[string][]browserProfilePath)
	for _, pointer := range request.Paths {
		if path, supported := parseBrowserResumePath(pointer); supported {
			webPaths[path.resumeID] = append(webPaths[path.resumeID], path)
			continue
		}
		if path, supported := parseBrowserProfilePath(pointer); supported {
			profilePaths[path.resumeID] = append(profilePaths[path.resumeID], path)
			continue
		}
		if resumeID, supported := hhAboutResumeID(pointer); supported {
			aboutIDs[resumeID] = struct{}{}
			continue
		}
		return core.ProfileStateObservation{}, operationError(core.ErrorUnsupported, operation, "HH browser reader does not support one or more declared profile state paths", nil)
	}
	if len(webPaths) == 0 && len(profilePaths) == 0 && len(aboutIDs) == 0 {
		return core.ProfileStateObservation{}, errors.New("HH browser profile state reader requires declared paths")
	}
	resumes := make(map[string]any, len(webPaths)+len(aboutIDs))
	if len(webPaths) != 0 {
		documents, err := client.readBrowserResumeDocuments(ctx, browserResumeIDs(webPaths))
		if err != nil {
			return core.ProfileStateObservation{}, err
		}
		for _, resumeID := range browserResumeIDs(webPaths) {
			observed := make(map[string]any)
			for _, path := range webPaths[resumeID] {
				value, exists := documents[resumeID][browserResumeReadField(path.field)]
				if !exists {
					continue
				}
				value, err = normalizeBrowserResumeField(path.field, value)
				if err != nil {
					return core.ProfileStateObservation{}, err
				}
				if len(path.fields) != 0 {
					object, ok := value.(map[string]any)
					if !ok {
						continue
					}
					value, exists = lookupObjectPath(object, path.fields)
					if !exists {
						continue
					}
				}
				setObjectPath(observed, append([]string{"web", path.field}, path.fields...), value)
			}
			resumes[resumeID] = observed
		}
	}
	for _, resumeID := range browserProfileResumeIDs(profilePaths) {
		document, err := client.getBrowserProfileDocument(ctx, resumeID)
		if err != nil {
			return core.ProfileStateObservation{}, err
		}
		observed, _ := resumes[resumeID].(map[string]any)
		if observed == nil {
			observed = make(map[string]any)
		}
		for _, path := range profilePaths[resumeID] {
			value, exists := document[path.field]
			if !exists {
				continue
			}
			value = normalizeBrowserProfileField(value)
			if len(path.fields) != 0 {
				object, ok := value.(map[string]any)
				if !ok {
					continue
				}
				value, exists = lookupObjectPath(object, path.fields)
				if !exists {
					continue
				}
			}
			setObjectPath(observed, append([]string{"web_profile", path.field}, path.fields...), value)
		}
		resumes[resumeID] = observed
	}
	aboutResumeIDs := make([]string, 0, len(aboutIDs))
	for resumeID := range aboutIDs {
		aboutResumeIDs = append(aboutResumeIDs, resumeID)
	}
	sort.Strings(aboutResumeIDs)
	for _, resumeID := range aboutResumeIDs {
		endpoint := strings.TrimRight(client.webBaseURL, "/") + "/resume/edit/" + url.PathEscape(resumeID) + "/about"
		document, finalURL, err := client.getHTML(ctx, endpoint, operation)
		if err != nil {
			return core.ProfileStateObservation{}, err
		}
		if isLoginURL(finalURL) {
			return core.ProfileStateObservation{}, operationError(core.ErrorUnauthorized, operation, "HH browser session requires authentication", nil)
		}
		aboutNode := findHTMLByQA(document, "resume-editor-about")
		if aboutNode == nil || aboutNode.Data != "textarea" {
			return core.ProfileStateObservation{}, operationError(core.ErrorPermanentFailure, operation, "HH resume edit page has no about field", nil)
		}
		about := htmlRawText(aboutNode)
		var normalized any = about
		if about == "" {
			normalized = nil
		}
		observed, _ := resumes[resumeID].(map[string]any)
		if observed == nil {
			observed = make(map[string]any)
		}
		observed["about"] = normalized
		resumes[resumeID] = observed
	}
	state, err := json.Marshal(map[string]any{"resumes": resumes})
	if err != nil {
		return core.ProfileStateObservation{}, fmt.Errorf("encode HH profile state observation: %w", err)
	}
	return core.NewProfileStateObservation(request.ProfileID, state, "", time.Now().UTC())
}

func (client *BrowserReadClient) getHTML(ctx context.Context, endpoint, operation string) (*html.Node, *url.URL, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	transport, err := NewResumeTouchTransport(client.stateFile, client.httpClient)
	if err != nil {
		return nil, nil, err
	}
	transport.profileURL = endpoint
	transport.touchURL = endpoint
	httpClient, err := transport.authenticatedClient()
	if err != nil {
		return nil, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("create HH browser read request: %w", err)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	request.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
	request.Header.Set("User-Agent", client.userAgent)
	response, err := httpClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, ctxErr
		}
		return nil, nil, operationError(core.ErrorTemporaryFailure, operation, "HH browser read failed", err)
	}
	defer response.Body.Close()
	if err := classifyBrowserReadResponse(response, operation); err != nil {
		return nil, response.Request.URL, err
	}
	document, err := html.Parse(io.LimitReader(response.Body, maxBrowserHTMLResponse))
	if err != nil {
		return nil, response.Request.URL, operationError(core.ErrorTemporaryFailure, operation, "HH returned invalid HTML", err)
	}
	return document, response.Request.URL, nil
}

// isBrowserResourceUnavailable reports the 403 classification produced by
// classifyBrowserReadResponse: the resource exists but is not visible to the
// account, which is a platform-imposed skip for vacancy reads.
func isBrowserResourceUnavailable(err error) bool {
	var operationError *core.OperationError
	return errors.As(err, &operationError) && operationError.Category == core.ErrorPermanentFailure &&
		strings.Contains(operationError.Message, "not accessible for this account")
}

func classifyBrowserReadResponse(response *http.Response, operation string) error {
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return nil
	case response.StatusCode == http.StatusUnauthorized:
		drain(response.Body)
		return operationError(core.ErrorUnauthorized, operation, "HH browser session was rejected", nil)
	case response.StatusCode == http.StatusForbidden:
		// A 403 on one page while other reads still work means the resource is
		// not accessible for this account, not that the session expired.
		drain(response.Body)
		return operationError(core.ErrorPermanentFailure, operation, "HH browser resource is not accessible for this account", nil)
	case response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone:
		drain(response.Body)
		return operationError(core.ErrorPermanentFailure, operation, "HH browser resource is unavailable", nil)
	case response.StatusCode == http.StatusTooManyRequests:
		drain(response.Body)
		failure := operationError(core.ErrorRateLimited, operation, "HH browser read was rate limited", nil)
		failure.RetryAfter = retryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
		return failure
	case response.StatusCode >= 500:
		drain(response.Body)
		return operationError(core.ErrorTemporaryFailure, operation, fmt.Sprintf("HH returned status %d", response.StatusCode), nil)
	default:
		drain(response.Body)
		return operationError(core.ErrorPermanentFailure, operation, fmt.Sprintf("HH rejected browser read with status %d", response.StatusCode), nil)
	}
}

func vacancyIDFromURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || !strings.HasPrefix(parsed.Path, "/vacancy/") {
		return ""
	}
	value = path.Base(parsed.Path)
	if value == "." || value == "/" {
		return ""
	}
	return value
}

func isLoginURL(value *url.URL) bool {
	return value != nil && strings.Contains(value.Path, "/account/login")
}

func findHTMLByQA(root *html.Node, value string) *html.Node {
	return findHTMLNode(root, func(node *html.Node) bool { return htmlAttribute(node, "data-qa") == value })
}

func findHTMLNode(root *html.Node, match func(*html.Node) bool) *html.Node {
	if root == nil {
		return nil
	}
	if match(root) {
		return root
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if found := findHTMLNode(child, match); found != nil {
			return found
		}
	}
	return nil
}

func walkHTML(root *html.Node, visit func(*html.Node) bool) {
	if root == nil || !visit(root) {
		return
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		walkHTML(child, visit)
	}
}

func htmlAttribute(node *html.Node, key string) string {
	if node == nil {
		return ""
	}
	for _, attribute := range node.Attr {
		if attribute.Key == key {
			return attribute.Val
		}
	}
	return ""
}

// browserVacancyPageLooksClosed recognises the archived or unavailable vacancy
// page, which has no title but explains the closure in its body.
func browserVacancyPageLooksClosed(document *html.Node) bool {
	rendered, err := renderHTMLNodeForText(document)
	if err != nil {
		return false
	}
	text := strings.ToLower(rendered)
	for _, marker := range []string{"вакансия закрыта", "вакансия не найдена", "вакансия удалена", "архив"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func renderHTMLNodeForText(node *html.Node) (string, error) {
	var builder strings.Builder
	if err := html.Render(&builder, node); err != nil {
		return "", err
	}
	return builder.String(), nil
}

func htmlText(node *html.Node) string {
	if node == nil {
		return ""
	}
	parts := make([]string, 0)
	walkHTML(node, func(current *html.Node) bool {
		if current.Type == html.TextNode {
			parts = append(parts, current.Data)
		}
		return true
	})
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

func htmlRawText(node *html.Node) string {
	if node == nil {
		return ""
	}
	var value strings.Builder
	walkHTML(node, func(current *html.Node) bool {
		if current.Type == html.TextNode {
			value.WriteString(current.Data)
		}
		return true
	})
	return value.String()
}

func hhAboutResumeID(pointer string) (string, bool) {
	if !strings.HasPrefix(pointer, "/") {
		return "", false
	}
	segments := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	if len(segments) != 3 || segments[0] != "resumes" || segments[2] != "about" {
		return "", false
	}
	resumeID, ok := unescapeJSONPointerSegment(segments[1])
	return resumeID, ok && strings.TrimSpace(resumeID) != ""
}

func unescapeJSONPointerSegment(value string) (string, bool) {
	var result strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '~' {
			result.WriteByte(value[index])
			continue
		}
		if index+1 >= len(value) {
			return "", false
		}
		index++
		switch value[index] {
		case '0':
			result.WriteByte('~')
		case '1':
			result.WriteByte('/')
		default:
			return "", false
		}
	}
	return result.String(), true
}
