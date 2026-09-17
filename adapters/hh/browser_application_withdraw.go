package hh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

const (
	negotiationDeclinePath = "/applicant/negotiations/decline"
	negotiationTrashPath   = "/applicant/negotiations/trash"
	maxWithdrawBodyBytes   = 4 << 10
)

var _ adapter.ApplicationWithdrawer = (*BrowserReadClient)(nil)

// WithdrawApplication cancels a pending response or hides a closed negotiation
// through the logged-in browser session. Web removal has two shapes: a pending
// response is declined, everything else (rejections, hidden topics, unknown
// states) is moved to the archive with the trash action, which also hides the
// negotiation chat. The caller removes the local record only after this
// succeeds.
//
// The web form field is `topic`: HH answers `{}` when the action is applied
// and a generic `<doc/>` document when the topic id is not recognised. A
// wrong field name still returns 200, so only the JSON body distinguishes the
// outcome and the caller must not treat `<doc/>` as success.
func (client *BrowserReadClient) WithdrawApplication(ctx context.Context, profileID core.ProfileID, state core.ApplicationPlatformState) (adapter.ApplicationWithdrawalResult, error) {
	if profileID == "" || profileID != client.profileID {
		return adapter.ApplicationWithdrawalResult{}, errors.New("HH browser withdrawal profile does not match")
	}
	negotiationID := strings.TrimSpace(state.ExternalNegotiationID)
	if negotiationID == "" {
		return adapter.ApplicationWithdrawalResult{}, &core.OperationError{
			Category: core.ErrorPermanentFailure, Operation: "applications.withdraw",
			Message: "application has no negotiation identity",
		}
	}
	action := "trash"
	values := url.Values{"topic": {negotiationID}, "substate": {"HIDE"}}
	if state.Disposition == core.ApplicationDispositionPending {
		action = "decline"
		values.Del("substate")
	}
	endpoint := strings.TrimRight(client.webBaseURL, "/")
	if action == "decline" {
		endpoint += negotiationDeclinePath
	} else {
		endpoint += negotiationTrashPath
	}
	status, body, err := client.postNegotiationAction(ctx, endpoint, values, "applications.withdraw")
	if err != nil {
		return adapter.ApplicationWithdrawalResult{}, err
	}
	if status < 200 || status >= 300 {
		category := core.ErrorPermanentFailure
		switch {
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			category = core.ErrorUnauthorized
		case status == http.StatusTooManyRequests:
			category = core.ErrorRateLimited
		case status >= 500:
			category = core.ErrorTemporaryFailure
		}
		return adapter.ApplicationWithdrawalResult{}, &core.OperationError{
			Category: category, Operation: "applications.withdraw", Platform: Name,
			Message: fmt.Sprintf("HH returned status %d", status), Cause: errors.New(strings.TrimSpace(body)),
		}
	}
	if !withdrawalApplied(body) {
		return adapter.ApplicationWithdrawalResult{}, &core.OperationError{
			Category: core.ErrorTemporaryFailure, Operation: "applications.withdraw", Platform: Name,
			Message: "HH did not confirm the negotiation action",
			Cause:   errors.New(strings.TrimSpace(body)),
		}
	}
	return adapter.ApplicationWithdrawalResult{Action: action}, nil
}

// withdrawalApplied reports whether HH confirmed the negotiation action. The
// web responds with an empty JSON document when the topic id was accepted; a
// generic `<doc/>` body means the request reached the site but matched
// nothing.
func withdrawalApplied(body string) bool {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return true
	}
	if strings.HasPrefix(trimmed, "<") {
		return false
	}
	return strings.HasPrefix(trimmed, "{")
}

func (client *BrowserReadClient) postNegotiationAction(ctx context.Context, endpoint string, values url.Values, operation string) (int, string, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	transport, err := NewResumeTouchTransport(client.stateFile, client.httpClient)
	if err != nil {
		return 0, "", err
	}
	transport.profileURL = endpoint
	transport.touchURL = endpoint
	httpClient, err := transport.authenticatedClient()
	if err != nil {
		return 0, "", err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return 0, "", fmt.Errorf("parse HH negotiation action endpoint: %w", err)
	}
	xsrf := cookieValue(httpClient, parsed, "_xsrf")
	if xsrf != "" && values.Get("_xsrf") == "" {
		values.Set("_xsrf", xsrf)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return 0, "", fmt.Errorf("create HH negotiation action request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Requested-With", "XMLHttpRequest")
	request.Header.Set("Referer", strings.TrimRight(client.webBaseURL, "/")+"/applicant/negotiations")
	request.Header.Set("Accept", "application/xml,application/json,text/html")
	request.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
	request.Header.Set("User-Agent", client.userAgent)
	if xsrf != "" {
		request.Header.Set("X-Xsrftoken", xsrf)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, "", ctxErr
		}
		return 0, "", operationError(core.ErrorTemporaryFailure, operation, "HH negotiation action failed", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxWithdrawBodyBytes))
	return response.StatusCode, string(body), nil
}
