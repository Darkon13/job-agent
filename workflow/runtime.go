package workflow

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

type Clock interface {
	Now() time.Time
}

type IDGenerator interface {
	NewID(prefix string) (string, error)
}

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

type RandomIDGenerator struct{}

func (RandomIDGenerator) NewID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "-" + hex.EncodeToString(value[:]), nil
}
