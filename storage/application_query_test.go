package storage_test

import (
	"testing"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func TestApplicationListGroupClassifiesValidationDecisions(t *testing.T) {
	cases := []struct {
		name  string
		code  string
		group string
	}{
		{"questionnaire", "questionnaire_required", "needs_input"},
		{"test", "vacancy_test_required", "needs_input"},
		{"extra data", "platform_validation_required", "needs_input"},
		{"captcha", "captcha_required", "needs_input"},
		{"unsupported flow", "unsupported_response_flow", "needs_input"},
		// A platform refusal is not an operator task: it belongs to the
		// unsent applications, not to the "needs input" filter.
		{"refusal", "response_impossible", "not_sent"},
		{"resume", "resume_not_suitable", "not_sent"},
	}
	for _, testCase := range cases {
		entry := storage.ApplicationListEntry{Status: core.ApplicationWaitingValidation, DecisionCode: testCase.code}
		if group := storage.ApplicationListGroup(entry); group != testCase.group {
			t.Fatalf("%s: group=%s want=%s", testCase.name, group, testCase.group)
		}
	}
}
