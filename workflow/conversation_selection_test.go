package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func TestSelectAndScheduleFollowUpUsesUnansweredStrategy(t *testing.T) {
	for _, test := range []struct {
		name     string
		strategy core.FollowUpSelectionStrategy
		want     core.ConversationID
	}{
		{name: "oldest", strategy: core.FollowUpSelectOldestUnanswered, want: "oldest"},
		{name: "newest", strategy: core.FollowUpSelectNewestUnanswered, want: "newest"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, repository, _, clock := newConversationWorkflowFixture(t)
			appendAwaitingEmployer(t, repository, "oldest", clock.now.Add(-96*time.Hour), nil)
			appendAwaitingEmployer(t, repository, "newest", clock.now.Add(-73*time.Hour), nil)
			incoming := clock.now.Add(-70 * time.Hour)
			appendAwaitingEmployer(t, repository, "applicant-must-reply", clock.now.Add(-90*time.Hour), &incoming)

			result, err := service.SelectAndScheduleFollowUp(context.Background(), followUpSelectionRequest(test.strategy, "selection-1"))
			if err != nil || !result.Selected || !result.Created || result.Conversation.ID != test.want {
				t.Fatalf("selection=%#v err=%v", result, err)
			}
			if result.FollowUp.AnchorMessageID != result.Conversation.LastMessageID || result.FollowUp.Content.Text == "" {
				t.Fatalf("follow-up=%#v conversation=%#v", result.FollowUp, result.Conversation)
			}
		})
	}
}

func TestSelectAndScheduleFollowUpIsIdempotentForRandomSelection(t *testing.T) {
	service, repository, _, clock := newConversationWorkflowFixture(t)
	appendAwaitingEmployer(t, repository, "first", clock.now.Add(-96*time.Hour), nil)
	appendAwaitingEmployer(t, repository, "second", clock.now.Add(-80*time.Hour), nil)
	request := followUpSelectionRequest(core.FollowUpSelectRandom, "random-run-1")

	first, err := service.SelectAndScheduleFollowUp(context.Background(), request)
	if err != nil || !first.Selected || !first.Created {
		t.Fatalf("first selection=%#v err=%v", first, err)
	}
	second, err := service.SelectAndScheduleFollowUp(context.Background(), request)
	if err != nil || !second.Selected || second.Created || second.Conversation.ID != first.Conversation.ID || second.FollowUp.ID != first.FollowUp.ID {
		t.Fatalf("second selection=%#v err=%v first=%#v", second, err, first)
	}
	followUps, err := repository.ListFollowUps(context.Background(), storage.FollowUpFilter{ProfileID: "profile-1"})
	if err != nil || len(followUps) != 1 {
		t.Fatalf("follow-ups=%#v err=%v", followUps, err)
	}
}

func TestSelectAndScheduleFollowUpDoesNothingWithoutEligibleConversation(t *testing.T) {
	service, repository, _, clock := newConversationWorkflowFixture(t)
	incoming := clock.now.Add(-24 * time.Hour)
	appendAwaitingEmployer(t, repository, "needs-applicant", clock.now.Add(-48*time.Hour), &incoming)
	result, err := service.SelectAndScheduleFollowUp(context.Background(), followUpSelectionRequest(core.FollowUpSelectOldestUnanswered, "empty-run"))
	if err != nil || result.Selected || result.Created {
		t.Fatalf("selection=%#v err=%v", result, err)
	}
}

func appendAwaitingEmployer(t *testing.T, repository storage.ConversationRepository, id core.ConversationID, outgoingAt time.Time, incomingAt *time.Time) {
	t.Helper()
	conversation, err := core.NewConversation(id, "hh", "profile-1", "external-"+string(id), outgoingAt.Add(-time.Hour))
	if err != nil {
		t.Fatalf("new conversation %s: %v", id, err)
	}
	if _, _, err := repository.CreateConversation(context.Background(), conversation); err != nil {
		t.Fatalf("store conversation %s: %v", id, err)
	}
	outgoing := core.ConversationMessage{
		ID: core.MessageID("outgoing-" + string(id)), ConversationID: id, Direction: core.MessageOutgoing,
		Kind: core.MessageText, Status: core.MessageSent, Text: "Добрый день, буду рад обсудить вакансию", OccurredAt: outgoingAt,
	}
	if _, _, err := repository.AppendConversationMessage(context.Background(), outgoing, outgoingAt); err != nil {
		t.Fatalf("append outgoing %s: %v", id, err)
	}
	if incomingAt == nil {
		return
	}
	incoming := core.ConversationMessage{
		ID: core.MessageID("incoming-" + string(id)), ConversationID: id, Direction: core.MessageIncoming,
		Kind: core.MessageText, Status: core.MessageObserved, Text: "Уточните, пожалуйста, опыт", OccurredAt: *incomingAt,
	}
	if _, _, err := repository.AppendConversationMessage(context.Background(), incoming, *incomingAt); err != nil {
		t.Fatalf("append incoming %s: %v", id, err)
	}
}

func followUpSelectionRequest(strategy core.FollowUpSelectionStrategy, key string) SelectFollowUpRequest {
	return SelectFollowUpRequest{
		ProfileID: "profile-1", Strategy: strategy, MinimumSilence: 72 * time.Hour,
		RunAfter: time.Minute, DeadlineAfter: 24 * time.Hour,
		Content: core.MessageContent{Text: "Добрый день! Подскажите, пожалуйста, актуальна ли ещё вакансия?"},
		Policy: core.FollowUpPolicy{
			CancelOnIncoming: true, RequireActiveConversation: true,
			MaxFollowUps: 1, Cooldown: core.Duration(72 * time.Hour),
		},
		IdempotencyKey: key,
	}
}
