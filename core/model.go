package core

import (
	"errors"
	"fmt"
	"time"
)

type VacancyState string

const (
	VacancyStateUnknown  VacancyState = "unknown"
	VacancyStateOpen     VacancyState = "open"
	VacancyStateArchived VacancyState = "archived"
)

// Vacancy is the normalized representation returned by every job platform.
type Vacancy struct {
	Platform    Platform       `json:"platform"`
	ExternalID  string         `json:"external_id"`
	URL         string         `json:"url"`
	Title       string         `json:"title"`
	Employer    string         `json:"employer,omitempty"`
	State       VacancyState   `json:"state"`
	PublishedAt *time.Time     `json:"published_at,omitempty"`
	ObservedAt  time.Time      `json:"observed_at"`
	Attributes  map[string]any `json:"attributes,omitempty"`
}

func (vacancy Vacancy) Key() VacancyKey {
	return VacancyKey{Platform: vacancy.Platform, ExternalID: vacancy.ExternalID}
}

func (vacancy Vacancy) Validate() error {
	if err := vacancy.Key().Validate(); err != nil {
		return err
	}
	if vacancy.Title == "" {
		return ErrVacancyTitleRequired
	}
	if vacancy.ObservedAt.IsZero() {
		return ErrVacancyObservedAtRequired
	}
	switch vacancy.State {
	case VacancyStateUnknown, VacancyStateOpen, VacancyStateArchived:
		return nil
	default:
		return ErrVacancyStateInvalid
	}
}

// SearchPage supports both page-number and cursor-based platforms.
type SearchPage struct {
	Vacancies  []Vacancy `json:"vacancies"`
	NextCursor string    `json:"next_cursor,omitempty"`
	Done       bool      `json:"done"`
}

func (page SearchPage) Validate() error {
	if page.Done && page.NextCursor != "" {
		return errors.New("completed search page must not contain next cursor")
	}
	seen := make(map[string]struct{}, len(page.Vacancies))
	for index, vacancy := range page.Vacancies {
		if err := vacancy.Validate(); err != nil {
			return fmt.Errorf("vacancy %d: %w", index, err)
		}
		key := vacancy.Key().String()
		if _, exists := seen[key]; exists {
			return fmt.Errorf("search page contains duplicate vacancy %s", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// VacancyDiscovery records that one search/profile context observed a vacancy.
// It is separate from Vacancy so the normalized vacancy is stored only once.
type VacancyDiscovery struct {
	VacancyKey   VacancyKey `json:"vacancy_key"`
	SearchID     SearchID   `json:"search_id"`
	ProfileID    ProfileID  `json:"profile_id,omitempty"`
	DiscoveredAt time.Time  `json:"discovered_at"`
}

func (discovery VacancyDiscovery) Validate() error {
	if err := discovery.VacancyKey.Validate(); err != nil {
		return err
	}
	if discovery.SearchID == "" {
		return errors.New("vacancy discovery requires search id")
	}
	if discovery.DiscoveredAt.IsZero() {
		return errors.New("vacancy discovery requires discovered_at")
	}
	return nil
}

var (
	ErrVacancyTitleRequired      = domainError("vacancy requires title")
	ErrVacancyObservedAtRequired = domainError("vacancy requires observed_at")
	ErrVacancyStateInvalid       = domainError("vacancy has invalid state")
)

type domainError string

func (err domainError) Error() string { return string(err) }
