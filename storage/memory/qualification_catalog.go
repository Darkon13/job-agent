package memory

import (
	"context"
	"errors"
	"sort"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

var _ storage.QualificationCatalogRepository = (*Repository)(nil)

type qualificationOfferingKey struct {
	platform  core.Platform
	profileID core.ProfileID
	offering  core.QualificationID
}

func (repository *Repository) UpsertQualificationOfferings(ctx context.Context, platform core.Platform, profileID core.ProfileID, offerings []core.QualificationOffering) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, offering := range offerings {
		if err := offering.Validate(); err != nil {
			return err
		}
		if offering.Platform != platform || offering.ProfileID != profileID {
			return errors.New("qualification offering belongs to another profile")
		}
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	for _, offering := range offerings {
		key := qualificationOfferingKey{platform: platform, profileID: profileID, offering: offering.ID}
		if stored, exists := repository.qualificationOfferings[key]; exists && stored.ObservedAt.After(offering.ObservedAt) {
			continue
		}
		repository.qualificationOfferings[key] = cloneQualificationOffering(offering)
	}
	return nil
}

func (repository *Repository) QualificationOfferings(ctx context.Context, platform core.Platform, profileID core.ProfileID) ([]core.QualificationOffering, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	offerings := make([]core.QualificationOffering, 0)
	for key, offering := range repository.qualificationOfferings {
		if key.platform != platform || key.profileID != profileID {
			continue
		}
		cloned := cloneQualificationOffering(offering)
		if best, exists := repository.qualificationBest[qualificationKey{
			platform: platform, profileID: profileID,
			familyID: offering.Qualification.FamilyID, levelID: offering.Qualification.LevelID,
		}]; exists {
			value := best
			cloned.BestResult = &value
		}
		offerings = append(offerings, cloned)
	}
	sort.Slice(offerings, func(i, j int) bool {
		left, right := offerings[i], offerings[j]
		if left.Qualification.FamilyID != right.Qualification.FamilyID {
			return left.Qualification.FamilyID < right.Qualification.FamilyID
		}
		leftOrder, rightOrder := qualificationOfferingOrder(left), qualificationOfferingOrder(right)
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		return left.ID < right.ID
	})
	return offerings, nil
}

func cloneQualificationOffering(source core.QualificationOffering) core.QualificationOffering {
	result := source
	if source.Qualification.LevelOrder != nil {
		order := *source.Qualification.LevelOrder
		result.Qualification.LevelOrder = &order
	}
	if source.BestResult != nil {
		best := *source.BestResult
		if source.BestResult.Score != nil {
			score := *source.BestResult.Score
			best.Score = &score
		}
		if source.BestResult.MaxScore != nil {
			maxScore := *source.BestResult.MaxScore
			best.MaxScore = &maxScore
		}
		result.BestResult = &best
	}
	return result
}

func qualificationOfferingOrder(offering core.QualificationOffering) int {
	if offering.Qualification.LevelOrder == nil {
		return 1 << 30
	}
	return *offering.Qualification.LevelOrder
}
