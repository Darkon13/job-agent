package core

import (
	"encoding/json"
	"errors"
	"time"
)

// Duration keeps declarative JSON human-readable while retaining time.Duration
// semantics inside the domain.
type Duration time.Duration

func (duration Duration) Value() time.Duration {
	return time.Duration(duration)
}

func (duration Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(duration).String())
}

func (duration *Duration) UnmarshalJSON(data []byte) error {
	if duration == nil {
		return errors.New("duration is nil")
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return errors.New("duration must be a string")
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	*duration = Duration(parsed)
	return nil
}
