package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapters/hh"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	"github.com/Darkon13/job-agent/storage/memory"
)

type qualificationFakeBrowser struct {
	selects [][]string
	nexts   int
}

func (browser *qualificationFakeBrowser) OpenOffering(context.Context, string, string, string) (hh.QualificationOffering, error) {
	return hh.QualificationOffering{}, nil
}

func (browser *qualificationFakeBrowser) StartAttempt(context.Context) (hh.QualificationCapture, error) {
	return qualificationQuestion(), nil
}

func (browser *qualificationFakeBrowser) Select(_ context.Context, _ string, optionIDs []string) (hh.QualificationCapture, error) {
	browser.selects = append(browser.selects, append([]string(nil), optionIDs...))
	capture := qualificationQuestion()
	capture.SelectedOptionIDs = append([]string(nil), optionIDs...)
	return capture, nil
}

func (browser *qualificationFakeBrowser) Next(_ context.Context, _ string, optionIDs []string) (hh.QualificationCapture, error) {
	if len(optionIDs) != 1 || optionIDs[0] != "runtime-a" {
		return hh.QualificationCapture{}, fmt.Errorf("unexpected confirmed ids: %v", optionIDs)
	}
	browser.nexts++
	return hh.QualificationCapture{Status: "completed"}, nil
}

func qualificationQuestion() hh.QualificationCapture {
	return hh.QualificationCapture{
		Status: "question", Progress: hh.QualificationProgress{Current: 1, Total: 1},
		Question: core.Question{ID: "runtime-question-1", Text: "Choose", Kind: core.QuestionSingle,
			Options: []core.QuestionOption{{ID: "runtime-a", Text: "Alpha"}, {ID: "runtime-b", Text: "Beta"}}},
	}
}

type qualificationFixedClock struct{ now time.Time }

func (clock qualificationFixedClock) Now() time.Time { return clock.now }

type qualificationSequenceIDs struct{ next int }

func (ids *qualificationSequenceIDs) NewID(prefix string) (string, error) {
	ids.next++
	return fmt.Sprintf("%s-%d", prefix, ids.next), nil
}

func TestReviewAttemptAllowsReselectionAndPersistsOnlyConfirmedChoice(t *testing.T) {
	ctx := context.Background()
	repository := memory.NewRepository()
	browser := &qualificationFakeBrowser{}
	input := bufio.NewScanner(strings.NewReader("2\nr\n1\n\n"))
	var output bytes.Buffer
	offering := hh.QualificationOffering{
		SkillID: "510338", FamilyName: "Docker", Level: "Базовый", Kind: "theory", StartAvailable: true,
	}
	result, err := reviewAttempt(ctx, repository, browser,
		qualificationFixedClock{now: time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)},
		&qualificationSequenceIDs{}, "primary", offering, input, &output)
	if err != nil {
		t.Fatalf("review attempt: %v\n%s", err, output.String())
	}
	if result.Answers != 1 || result.KnownQuestions != 1 || browser.nexts != 1 || len(browser.selects) != 2 {
		t.Fatalf("unexpected result=%#v selects=%v nexts=%d", result, browser.selects, browser.nexts)
	}
	if browser.selects[0][0] != "runtime-b" || browser.selects[1][0] != "runtime-a" {
		t.Fatalf("reselection did not reach browser: %v", browser.selects)
	}
	session, err := repository.ReviewSession(ctx, result.SessionID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if session.Status != core.ReviewCompleted {
		t.Fatalf("unexpected session status %s", session.Status)
	}
	selections, err := repository.ReviewSelections(ctx, result.SessionID)
	if err != nil {
		t.Fatalf("load selections: %v", err)
	}
	if len(selections) != 1 || len(selections[0].SelectedOptions) != 1 || selections[0].SelectedOptions[0] != "Alpha" {
		t.Fatalf("unexpected durable selections: %#v", selections)
	}
	definitions, err := repository.ListTestDefinitions(ctx, storage.TestDefinitionFilter{Platform: "hh", FamilyID: "skill:510338"})
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if len(definitions) != 1 || len(definitions[0].Questions) != 1 || len(definitions[0].Questions[0].Options) != 2 {
		t.Fatalf("question variants were not retained: %#v", definitions)
	}
}

func TestParseOptionIndexesSupportsMultipleAndDeduplicates(t *testing.T) {
	indexes, err := parseOptionIndexes("3, 1;3", 3, core.QuestionMultiple)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if fmt.Sprint(indexes) != "[1 3]" {
		t.Fatalf("unexpected indexes %v", indexes)
	}
	if _, err := parseOptionIndexes("1,2", 3, core.QuestionSingle); err == nil {
		t.Fatal("single-choice parser accepted two options")
	}
}
