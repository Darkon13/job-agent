package hh

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/Darkon13/job-agent/adapters/hhapi"
	"github.com/Darkon13/job-agent/core"
)

// readMobileVacancy normalizes the Android API vacancy card for the application
// worker. It keeps the same core attributes as the web reader so platform
// preflight, filtering and letter context keep working without a browser GET.
func readMobileVacancy(ctx context.Context, api *hhapi.Client, profileID core.ProfileID, key core.VacancyKey) (core.Vacancy, error) {
	if api == nil {
		return core.Vacancy{}, operationError(core.ErrorUnsupported, "vacancies.read.hhapi", "HH mobile API is not configured", nil)
	}
	if err := key.Validate(); err != nil {
		return core.Vacancy{}, err
	}
	if key.Platform != Name {
		return core.Vacancy{}, errors.New("HH mobile vacancy reader requires an HH vacancy")
	}
	item, err := api.Vacancy(ctx, key.ExternalID)
	if err != nil {
		var captcha *hhapi.CaptchaRequiredError
		if errors.As(err, &captcha) {
			if captcha.URL != "" {
				_ = os.WriteFile(captchaPendingFilePath, []byte(captcha.URL), 0o644)
			}
			retryAt := time.Now().Add(10 * time.Minute)
			return core.Vacancy{}, &core.OperationError{
				Category: core.ErrorRateLimited, Operation: "vacancies.read.hhapi",
				Platform: Name, RetryAfter: &retryAt, Message: "HH requires captcha: " + captcha.Error(),
			}
		}
		return core.Vacancy{}, err
	}
	if item.ID != key.ExternalID || item.Name == "" {
		return core.Vacancy{}, operationError(core.ErrorPermanentFailure, "vacancies.read.hhapi", "HH mobile API returned an invalid vacancy", nil)
	}
	state := core.VacancyStateOpen
	if item.Archived {
		state = core.VacancyStateArchived
	}
	attributes := map[string]any{
		"has_test":                 item.HasTest,
		"response_letter_required": item.ResponseLetter,
		"closed_for_applicants":    item.ClosedForApplicants,
		"allow_messages":           item.AllowMessages,
	}
	if item.Employer.ID != "" {
		attributes["employer_id"] = item.Employer.ID
	}
	if item.Description != "" {
		attributes["description"] = item.Description
	}
	if len(item.Relations) != 0 {
		attributes["relations"] = append([]string(nil), item.Relations...)
	}
	if len(item.KeySkills) != 0 {
		skills := make([]string, 0, len(item.KeySkills))
		for _, skill := range item.KeySkills {
			if skill.Name != "" {
				skills = append(skills, skill.Name)
			}
		}
		if len(skills) != 0 {
			attributes["key_skills"] = skills
		}
	}
	if len(item.WorkFormat) != 0 {
		formats := make([]map[string]string, 0, len(item.WorkFormat))
		for _, format := range item.WorkFormat {
			if format.ID != "" {
				formats = append(formats, map[string]string{"id": format.ID})
			}
		}
		if len(formats) != 0 {
			attributes["work_format"] = formats
		}
	}
	observedAt := time.Now().UTC()
	publishedAt, err := parseHHTime(item.PublishedAt)
	if err != nil {
		return core.Vacancy{}, err
	}
	vacancy := core.Vacancy{
		Platform: Name, ExternalID: item.ID, URL: item.AlternateURL, Title: item.Name,
		Employer: item.Employer.Name, State: state, PublishedAt: publishedAt,
		ObservedAt: observedAt, Attributes: attributes,
	}
	if err := vacancy.Validate(); err != nil {
		return core.Vacancy{}, err
	}
	return vacancy, nil
}
