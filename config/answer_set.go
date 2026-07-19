package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/Darkon13/job-agent/core"
)

// LoadAnswerBlock loads one small, portable group of reviewed answers.
// Unknown fields are rejected to catch stale or misspelled configuration.
func LoadAnswerBlock(path string) (core.AnswerBlock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return core.AnswerBlock{}, fmt.Errorf("read answer block: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var block core.AnswerBlock
	if err := decoder.Decode(&block); err != nil {
		return core.AnswerBlock{}, fmt.Errorf("decode answer block: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return core.AnswerBlock{}, fmt.Errorf("decode answer block: unexpected trailing JSON value")
		}
		return core.AnswerBlock{}, fmt.Errorf("decode answer block trailing data: %w", err)
	}
	if err := core.ValidateAnswerBlock(block); err != nil {
		return core.AnswerBlock{}, fmt.Errorf("validate answer block: %w", err)
	}
	return block, nil
}
