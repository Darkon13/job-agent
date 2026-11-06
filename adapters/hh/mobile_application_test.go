package hh

import (
	"errors"
	"testing"

	"github.com/Darkon13/job-agent/adapters/hhapi"
	"github.com/Darkon13/job-agent/core"
)

func TestClassifyMobileApplicationError(t *testing.T) {
	command := applicationCommand()
	tests := []struct {
		name     string
		err      error
		category core.ErrorCategory
		code     string
		applied  bool
		already  bool
	}{
		{name: "already", err: &hhapi.APIError{StatusCode: 400, Body: `{"errors":[{"value":"already_applied"}]}`}, applied: true, already: true},
		{name: "quota", err: &hhapi.APIError{StatusCode: 409, Body: `{"errors":[{"value":"limit_exceeded"}]}`}, category: core.ErrorQuotaExceeded},
		{name: "test", err: &hhapi.APIError{StatusCode: 400, Body: `{"errors":[{"value":"test_required"}]}`}, category: core.ErrorValidationRequired, code: "questionnaire_required"},
		{name: "resume", err: &hhapi.APIError{StatusCode: 400, Body: `{"errors":[{"value":"resume_not_found"}]}`}, category: core.ErrorValidationRequired, code: "resume_not_suitable"},
		{name: "unauthorized", err: &hhapi.APIError{StatusCode: 401}, category: core.ErrorUnauthorized},
		{name: "rate limit", err: &hhapi.APIError{StatusCode: 429}, category: core.ErrorRateLimited},
		{name: "server", err: &hhapi.APIError{StatusCode: 503}, category: core.ErrorAmbiguousResult},
		{name: "network", err: errors.New("connection reset"), category: core.ErrorAmbiguousResult},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := classifyMobileApplicationError(command, test.err)
			if test.applied || test.already {
				if err != nil || result.Applied != test.applied || result.AlreadyApplied != test.already {
					t.Fatalf("result=%#v err=%v", result, err)
				}
				return
			}
			if !core.ErrorIsCategory(err, test.category) {
				t.Fatalf("error=%v, want %s", err, test.category)
			}
			if test.code != "" {
				var operationError *core.OperationError
				if !errors.As(err, &operationError) || operationError.Metadata["code"] != test.code {
					t.Fatalf("operation error = %#v", operationError)
				}
			}
		})
	}
}
