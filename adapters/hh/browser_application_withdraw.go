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
	values := url.Values{"topic_id": {negotiationID}, "substate": {"HIDE"}}
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
	return adapter.ApplicationWithdrawalResult{Action: action}, nil
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
