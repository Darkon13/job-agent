package hhapi

import (
	"context"
	"fmt"
	"sort"
)

type Salary struct {
	From     *float64 `json:"from"`
	To       *float64 `json:"to"`
	Currency string   `json:"currency"`
}

func (v Vacancy) SalaryMidpoint() (float64, bool) {
	if v.Salary == nil || v.Salary.Currency != "RUR" {
		return 0, false
	}
	from, to := v.Salary.From, v.Salary.To
	switch {
	case from != nil && to != nil:
		return (*from + *to) / 2, true
	case from != nil:
		return *from, true
	case to != nil:
		return *to, true
	}
	return 0, false
}

// SalaryRange оценивает вилку по медиане похожих удалённых вакансий роли.
func (c *Client) SalaryRange(ctx context.Context, vacancyID string) (int, int, error) {
	vacancy, err := c.Vacancy(ctx, vacancyID)
	if err != nil {
		return 0, 0, err
	}
	if mid, ok := vacancy.SalaryMidpoint(); ok {
		return int(mid * 0.9), int(mid * 1.1), nil
	}
	role := ""
	if len(vacancy.ProfessionalRoles) > 0 {
		role = vacancy.ProfessionalRoles[0].ID
	}
	path := "/vacancies?per_page=100&only_with_salary=true&work_format=REMOTE&area=113"
	if role != "" {
		path += "&professional_role=" + role
	} else {
		path += "&text=" + vacancy.Name
	}
	var page struct {
		Items []struct {
			Salary *Salary `json:"salary"`
		} `json:"items"`
	}
	if err := c.Request(ctx, "GET", path, nil, &page); err != nil {
		return 0, 0, err
	}
	var values []float64
	for _, item := range page.Items {
		v := Vacancy{Salary: item.Salary}
		if mid, ok := v.SalaryMidpoint(); ok {
			values = append(values, mid)
		}
	}
	if len(values) < 3 {
		return 0, 0, fmt.Errorf("hh salary: not enough data (%d)", len(values))
	}
	sort.Float64s(values)
	median := values[len(values)/2]
	return int(median * 0.9), int(median * 1.1), nil
}
