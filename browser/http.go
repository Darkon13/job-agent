package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Darkon13/job-agent/core"
)

const (
	defaultMaxResponseBytes = 8 << 20
	defaultRequestTimeout   = 2 * time.Minute
)

type HTTPConfig struct {
	BaseURL          string
	Token            string
	Client           *http.Client
	MaxResponseBytes int64
}

// HTTPClient talks to the browser worker over HTTP JSON. Operations of one
// profile are serialized locally; the worker enforces the same invariant.
type HTTPClient struct {
	baseURL  string
	token    string
	http     *http.Client
	maxBytes int64
	locks    sync.Map
}

var _ Client = (*HTTPClient)(nil)

func NewHTTPClient(config HTTPConfig) (*HTTPClient, error) {
	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		return nil, errors.New("browser worker client requires base URL")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("browser worker base URL must be an absolute http(s) URL")
	}
	if strings.TrimSpace(config.Token) == "" {
		return nil, errors.New("browser worker client requires a token")
	}
	client := config.Client
	if client == nil {
		client = &http.Client{Timeout: defaultRequestTimeout}
	}
	maxBytes := config.MaxResponseBytes
	if maxBytes == 0 {
		maxBytes = defaultMaxResponseBytes
	}
	if maxBytes < 1 {
		return nil, errors.New("browser worker max response bytes must be positive")
	}
	return &HTTPClient{
		baseURL: strings.TrimRight(baseURL, "/"), token: strings.TrimSpace(config.Token),
		http: client, maxBytes: maxBytes,
	}, nil
}

func (client *HTTPClient) Health(ctx context.Context) (Health, error) {
	var health Health
	if err := client.fetch(ctx, http.MethodGet, "/healthz", nil, &health); err != nil {
		return Health{}, OperationError("browser.health", err)
	}
	return health, nil
}

func (client *HTTPClient) Ready(ctx context.Context) (Ready, error) {
	var ready Ready
	if err := client.fetch(ctx, http.MethodGet, "/readyz", nil, &ready); err != nil {
		return Ready{}, OperationError("browser.ready", err)
	}
	return ready, nil
}

func (client *HTTPClient) Ensure(ctx context.Context, profileID core.ProfileID, request EnsureRequest) (ContextInfo, error) {
	var info ContextInfo
	err := client.withProfile(ctx, profileID, "browser.profiles.ensure", func() error {
		return client.fetch(ctx, http.MethodPost, profilePath(profileID)+"/ensure", request, &info)
	})
	if err != nil {
		return ContextInfo{}, err
	}
	return info, nil
}

func (client *HTTPClient) List(ctx context.Context) ([]ContextInfo, error) {
	var response struct {
		Profiles []ContextInfo `json:"profiles"`
	}
	if err := client.fetch(ctx, http.MethodGet, "/v1/profiles", nil, &response); err != nil {
		return nil, OperationError("browser.profiles.list", err)
	}
	return response.Profiles, nil
}

func (client *HTTPClient) Close(ctx context.Context, profileID core.ProfileID, purge bool) error {
	path := profilePath(profileID)
	if purge {
		path += "?purge=true"
	}
	return client.withProfile(ctx, profileID, "browser.profiles.close", func() error {
		return client.fetch(ctx, http.MethodDelete, path, nil, nil)
	})
}

func (client *HTTPClient) ExportStorageState(ctx context.Context, profileID core.ProfileID) (json.RawMessage, error) {
	var state json.RawMessage
	err := client.withProfile(ctx, profileID, "browser.state.export", func() error {
		return client.fetch(ctx, http.MethodGet, profilePath(profileID)+"/storage-state", nil, &state)
	})
	if err != nil {
		return nil, err
	}
	if !json.Valid(state) {
		return nil, OperationError("browser.state.export", invalidResponse("worker returned invalid storage state"))
	}
	return state, nil
}

func (client *HTTPClient) ImportStorageState(ctx context.Context, profileID core.ProfileID, state json.RawMessage) error {
	if !json.Valid(state) {
		return OperationError("browser.state.import", invalidResponse("storage state must be valid JSON"))
	}
	body := struct {
		Cookies json.RawMessage `json:"cookies"`
		Origins json.RawMessage `json:"origins"`
	}{}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(state, &root); err != nil {
		return OperationError("browser.state.import", invalidResponse("storage state must be an object"))
	}
	body.Cookies = root["cookies"]
	body.Origins = root["origins"]
	if len(body.Cookies) == 0 {
		body.Cookies = json.RawMessage("[]")
	}
	if len(body.Origins) == 0 {
		body.Origins = json.RawMessage("[]")
	}
	return client.withProfile(ctx, profileID, "browser.state.import", func() error {
		return client.fetch(ctx, http.MethodPut, profilePath(profileID)+"/storage-state", body, nil)
	})
}

func (client *HTTPClient) Goto(ctx context.Context, profileID core.ProfileID, request GotoRequest) (GotoResult, error) {
	var result GotoResult
	err := client.withProfile(ctx, profileID, "browser.page.goto", func() error {
		return client.fetch(ctx, http.MethodPost, profilePath(profileID)+"/goto", request, &result)
	})
	if err != nil {
		return GotoResult{}, err
	}
	return result, nil
}

func (client *HTTPClient) Page(ctx context.Context, profileID core.ProfileID) (PageInfo, error) {
	var info PageInfo
	err := client.withProfile(ctx, profileID, "browser.page.info", func() error {
		return client.fetch(ctx, http.MethodGet, profilePath(profileID)+"/page", nil, &info)
	})
	if err != nil {
		return PageInfo{}, err
	}
	return info, nil
}

func (client *HTTPClient) Content(ctx context.Context, profileID core.ProfileID, request ContentRequest) (ContentResult, error) {
	var result ContentResult
	err := client.withProfile(ctx, profileID, "browser.page.content", func() error {
		return client.fetch(ctx, http.MethodPost, profilePath(profileID)+"/content", request, &result)
	})
	if err != nil {
		return ContentResult{}, err
	}
	return result, nil
}

func (client *HTTPClient) Screenshot(ctx context.Context, profileID core.ProfileID, request ScreenshotRequest) ([]byte, error) {
	var data []byte
	err := client.withProfile(ctx, profileID, "browser.page.screenshot", func() error {
		body, err := client.request(ctx, http.MethodPost, profilePath(profileID)+"/screenshot", request, "image/png")
		if err != nil {
			return err
		}
		data = body
		return nil
	})
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (client *HTTPClient) Locator(ctx context.Context, profileID core.ProfileID, request LocatorRequest) error {
	return client.withProfile(ctx, profileID, "browser.page.locator", func() error {
		return client.fetch(ctx, http.MethodPost, profilePath(profileID)+"/locator", request, nil)
	})
}

func (client *HTTPClient) withProfile(ctx context.Context, profileID core.ProfileID, operation string, fn func() error) error {
	if profileID == "" {
		return OperationError(operation, invalidResponse("profile id is required"))
	}
	lock := client.profileLock(profileID)
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return OperationError(operation, err)
	}
	if err := fn(); err != nil {
		return OperationError(operation, err)
	}
	return nil
}

func (client *HTTPClient) profileLock(profileID core.ProfileID) *sync.Mutex {
	lock, _ := client.locks.LoadOrStore(profileID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func (client *HTTPClient) fetch(ctx context.Context, method, path string, body, target any) error {
	data, err := client.request(ctx, method, path, body, "application/json")
	if err != nil {
		return err
	}
	if target == nil {
		return nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return invalidResponse("worker returned invalid JSON: %v", err)
	}
	return nil
}

func (client *HTTPClient) request(ctx context.Context, method, path string, body any, accept string) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, invalidResponse("encode browser worker request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, reader)
	if err != nil {
		return nil, invalidResponse("create browser worker request: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if accept == "" {
		accept = "application/json"
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("X-Browser-Protocol", ProtocolVersion)

	response, err := client.http.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	defer response.Body.Close()
	if version := strings.TrimSpace(response.Header.Get("X-Browser-Protocol")); version != "" && version != ProtocolVersion {
		return nil, &Error{StatusCode: response.StatusCode, Code: "protocol", Message: "unsupported browser worker protocol " + version}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, client.maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > client.maxBytes {
		return nil, invalidResponse("browser worker response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope errorEnvelope
		if json.Unmarshal(data, &envelope) == nil && strings.TrimSpace(envelope.Error.Code) != "" {
			return nil, &Error{StatusCode: response.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message}
		}
		return nil, &Error{StatusCode: response.StatusCode, Code: "internal", Message: http.StatusText(response.StatusCode)}
	}
	return data, nil
}

func profilePath(profileID core.ProfileID) string {
	return "/v1/profiles/" + url.PathEscape(string(profileID))
}
