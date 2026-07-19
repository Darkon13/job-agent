package adapter

import (
	"context"
	"encoding/json"

	"github.com/Darkon13/job-agent/core"
)

type Descriptor interface {
	Name() string
	Capabilities() []core.Capability
}

type VacancySearcher interface {
	ValidateSearch(query json.RawMessage) error
	Search(ctx context.Context, profileID core.ProfileID, query json.RawMessage, cursor string) (core.SearchPage, error)
}

// Adapter is a facade over all transports used by one platform. Workflows
// depend on the smaller capability interfaces instead of this full facade.
type Adapter interface {
	Descriptor
	VacancySearcher
}

type Factory func(config json.RawMessage) (Adapter, error)
