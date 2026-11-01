package hhapi

import (
	"context"
	"fmt"
)

type ApplyRequest struct {
	VacancyID string `json:"vacancy_id"`
	ResumeID  string `json:"resume_id"`
	Message   string `json:"message,omitempty"`
}

type Negotiation struct {
	ID string `json:"id"`
}

// Apply sends a response with a cover letter through the mobile API.
func (c *Client) Apply(ctx context.Context, request ApplyRequest) (Negotiation, error) {
	var result Negotiation
	if err := c.Request(ctx, "POST", "/negotiations", request, &result); err != nil {
		return Negotiation{}, err
	}
	if result.ID == "" {
		return Negotiation{}, fmt.Errorf("hh api: response returned no negotiation id")
	}
	return result, nil
}
