package storage_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	"github.com/Darkon13/job-agent/storage/memory"
	"github.com/Darkon13/job-agent/storage/sqlite"
)

type gcStore interface {
	storage.VacancyRepository
	storage.ApplicationRepository
	storage.ApplicationReadRepository
	storage.ApplicationRemovalRepository
	storage.ApplicationPlatformStateRepository
	storage.ApplicationBudgetRepository
	storage.ApplicationPaceRepository
	storage.ApplicationCampaignRepository
	storage.ConversationRepository
	storage.ConversationPurgeRepository
	storage.ApplicationQueryRepository
}

func gcStores(t *testing.T, run func(*testing.T, gcStore)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) { run(t, memory.NewRepository()) })
	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "gc.db")
		if err := sqlite.MigrateUp(path); err != nil {
			t.Fatal(err)
		}
		repository, err := sqlite.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = repository.Close() })
		run(t, repository)
	})
}

func gcApplication(t *testing.T, repository gcStore, id, employer string, at time.Time) core.Application {
	t.Helper()
	ctx := context.Background()
	vacancy := core.Vacancy{Platform: "hh", ExternalID: id, Title: "Go developer", Employer: employer, State: core.VacancyStateOpen, ObservedAt: at}
	if _, err := repository.UpsertVacancy(ctx, vacancy); err != nil {
		t.Fatal(err)
	}
	application, err := core.NewApplication(core.ApplicationID(id), core.ApplicationKey{ProfileID: "primary", Vacancy: vacancy.Key()}, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.CreateApplication(ctx, application); err != nil {
		t.Fatal(err)
	}
	for _, status := range []core.ApplicationStatus{core.ApplicationPreparing, core.ApplicationReady, core.ApplicationSubmitting, core.ApplicationSubmitted} {
		previous := application.Status
		if err := application.Transition(status, application.UpdatedAt.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := repository.SaveApplication(ctx, application, previous); err != nil {
			t.Fatal(err)
		}
	}
	return application
}

func TestApplicationGCProtectsAccountingHistoryAndConversations(t *testing.T) {
	gcStores(t, func(t *testing.T, repository gcStore) {
		ctx := context.Background()
		now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
		a := gcApplication(t, repository, "a", "Sber", now.Add(-time.Hour))
		b := gcApplication(t, repository, "b", "Other", now.Add(-time.Hour))
		budget := core.ReserveApplicationBudgetParams{ApplicationID: a.ID, ProfileID: "primary", Platform: "hh", WindowStart: now.Truncate(24 * time.Hour), WindowEnd: now.Truncate(24 * time.Hour).Add(24 * time.Hour), Limit: 1, Now: now}
		if _, err := repository.ReserveApplicationBudget(ctx, budget); err != nil {
			t.Fatal(err)
		}
		if err := repository.CommitApplicationBudget(ctx, a.ID, now); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.AcquireApplicationPace(ctx, core.AcquireApplicationPaceParams{ApplicationID: a.ID, ProfileID: "primary", Platform: "hh", Interval: 25 * time.Second, Now: now}); err != nil {
			t.Fatal(err)
		}
		conversation, err := core.NewConversation("chat", "hh", "primary", "external-chat", now)
		if err != nil {
			t.Fatal(err)
		}
		conversation.ApplicationID = a.ID
		if _, _, err := repository.CreateConversation(ctx, conversation); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.AppendConversationMessage(ctx, core.ConversationMessage{ID: "m", ConversationID: "chat", Direction: core.MessageOutgoing, Kind: core.MessageText, Status: core.MessageSent, Text: "hello", OccurredAt: now}, now); err != nil {
			t.Fatal(err)
		}
		campaign, err := core.NewApplicationCampaign(core.NewApplicationCampaignParams{ID: "campaign", JobTag: "daily", Profiles: []core.ProfileID{"primary"}, Routes: []core.SearchID{"main"}, TargetSuccessful: 1, MaxInFlight: 1, CorrelationID: "correlation"}, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.CreateApplicationCampaign(ctx, campaign); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.LinkCampaignApplication(ctx, core.CampaignApplication{CampaignID: campaign.ID, ApplicationID: a.ID, DiscoveredAt: now}); err != nil {
			t.Fatal(err)
		}
		manual := core.ApplicationRemoval{Reason: core.ApplicationRemovalManual}
		if _, _, err := repository.RemoveApplication(ctx, a.ID, manual, now); !errors.Is(err, core.ErrApplicationInRunningCampaign) {
			t.Fatalf("running campaign item removal error = %v", err)
		}
		revision := campaign.Revision
		if err := campaign.Stop(core.ApplicationCampaignTargetReached, "done", now); err != nil {
			t.Fatal(err)
		}
		if err := repository.SaveApplicationCampaign(ctx, campaign, revision); err != nil {
			t.Fatal(err)
		}
		if _, removed, err := repository.RemoveApplication(ctx, a.ID, manual, now); err != nil || !removed {
			t.Fatalf("remove: %v %v", removed, err)
		}
		if _, removed, err := repository.RemoveApplication(ctx, a.ID, manual, now); err != nil || removed {
			t.Fatalf("retry: %v %v", removed, err)
		}
		if _, err := repository.ApplicationByID(ctx, a.ID); err == nil {
			t.Fatal("application content survived GC")
		}
		if conversations, err := repository.ListConversations(ctx, storage.ConversationFilter{ProfileID: "primary"}); err != nil || len(conversations) != 0 {
			t.Fatalf("chat survived GC: %v %v", conversations, err)
		}
		if states, err := repository.ListCampaignApplicationStates(ctx, campaign.ID); err != nil || len(states) != 1 || states[0].Application.Status != core.ApplicationSubmitted {
			t.Fatalf("history lost: %v %v", states, err)
		}
		budget.ApplicationID = b.ID
		if _, err := repository.ReserveApplicationBudget(ctx, budget); !core.ErrorIsCategory(err, core.ErrorQuotaExceeded) {
			t.Fatalf("GC reset quota: %v", err)
		}
		if pace, allowed, err := repository.AcquireApplicationPace(ctx, core.AcquireApplicationPaceParams{ApplicationID: b.ID, ProfileID: "primary", Platform: "hh", Interval: 25 * time.Second, Now: now}); err != nil || allowed || !pace.ScheduledAt.Equal(now.Add(25*time.Second)) {
			t.Fatalf("GC reset pacing: %v %v %v", pace, allowed, err)
		}
		a, _ = core.NewApplication("replacement", a.Key, now)
		if _, _, err := repository.CreateApplication(ctx, a); !errors.Is(err, storage.ErrApplicationRemoved) {
			t.Fatalf("GC reset dedup: %v", err)
		}
	})
}

func TestApplicationGCRechecksObservationInsideWriteTransaction(t *testing.T) {
	gcStores(t, func(t *testing.T, repository gcStore) {
		ctx := context.Background()
		now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
		a := gcApplication(t, repository, "a", "Sber", now.Add(-30*24*time.Hour))
		state := core.ApplicationPlatformState{ApplicationID: a.ID, ExternalNegotiationID: "n", PlatformState: "invitation", Disposition: core.ApplicationDispositionInvited, ObservedAt: now}
		if err := repository.SaveApplicationPlatformState(ctx, state); err != nil {
			t.Fatal(err)
		}
		request := core.ApplicationRemoval{Reason: core.ApplicationRemovalRetentionStale, StaleBefore: now.Add(-14 * 24 * time.Hour), ObservedAt: now.Add(-time.Minute)}
		if _, removed, err := repository.RemoveApplication(ctx, a.ID, request, now); err != nil || removed {
			t.Fatalf("deleted new invitation: %v %v", removed, err)
		}
		request.ObservedAt = now
		if _, removed, err := repository.RemoveApplication(ctx, a.ID, request, now); err != nil || removed {
			t.Fatalf("deleted invitation: %v %v", removed, err)
		}
		state.Disposition = core.ApplicationDispositionPending
		state.PlatformState = "response"
		if err := repository.SaveApplicationPlatformState(ctx, state); err != nil {
			t.Fatal(err)
		}
		if _, removed, err := repository.RemoveApplication(ctx, a.ID, request, now.Add(6*time.Minute)); err != nil || removed {
			t.Fatalf("accepted stale observation: %v %v", removed, err)
		}
		if _, removed, err := repository.RemoveApplication(ctx, a.ID, request, now); err != nil || !removed {
			t.Fatalf("eligible object: %v %v", removed, err)
		}
	})
}

func TestApplicationQueryFiltersWholeDatasetBeforePagination(t *testing.T) {
	gcStores(t, func(t *testing.T, repository gcStore) {
		ctx := context.Background()
		now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
		for i := 0; i < 205; i++ {
			employer := "Other"
			if i < 3 {
				employer = "Сбер"
			}
			gcApplication(t, repository, fmt.Sprintf("%03d", i), employer, now.Add(time.Duration(i)*time.Minute))
		}
		query := storage.ApplicationQuery{Employer: "СБЕР", Group: "state_unknown", Sort: "updated_asc", Limit: 2}
		page, err := repository.QueryApplications(ctx, query)
		if err != nil || page.Total != 3 || len(page.IDs) != 2 || page.IDs[0] != "000" {
			t.Fatalf("first page: %v %v", page, err)
		}
		query.Offset = 2
		page, err = repository.QueryApplications(ctx, query)
		if err != nil || page.Total != 3 || len(page.IDs) != 1 || page.IDs[0] != "002" {
			t.Fatalf("second page: %v %v", page, err)
		}
	})
}

func TestApplicationGCRemovesStaleQuestionnaireWithoutPlatformState(t *testing.T) {
	gcStores(t, func(t *testing.T, repository gcStore) {
		ctx := context.Background()
		now := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
		vacancy := core.Vacancy{Platform: "hh", ExternalID: "v-questionnaire", Title: "Go developer", State: core.VacancyStateOpen, ObservedAt: now.Add(-14 * 24 * time.Hour)}
		if _, err := repository.UpsertVacancy(ctx, vacancy); err != nil {
			t.Fatal(err)
		}
		application, err := core.NewApplication("application-stale-questionnaire", core.ApplicationKey{
			ProfileID: "primary", Vacancy: vacancy.Key(),
		}, now.Add(-14*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.CreateApplication(ctx, application); err != nil {
			t.Fatal(err)
		}
		if err := application.Transition(core.ApplicationPreparing, now.Add(-13*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := repository.SaveApplication(ctx, application, core.ApplicationNew); err != nil {
			t.Fatal(err)
		}
		application.DecisionCode = "questionnaire_required"
		if err := application.Transition(core.ApplicationWaitingValidation, now.Add(-13*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := repository.SaveApplication(ctx, application, core.ApplicationPreparing); err != nil {
			t.Fatal(err)
		}

		tombstone, removed, err := repository.RemoveApplication(ctx, application.ID,
			core.ApplicationRemoval{Reason: core.ApplicationRemovalRetentionValidation}, now)
		if err != nil || !removed || tombstone.Reason != core.ApplicationRemovalRetentionValidation {
			t.Fatalf("removed=%v tombstone=%#v err=%v", removed, tombstone, err)
		}
		if _, err := repository.ApplicationByID(ctx, application.ID); err == nil {
			t.Fatal("application still present after removal")
		}
	})
}

func TestOpenQuestionnaireStopsAfterParticipantLeft(t *testing.T) {
	gcStores(t, func(t *testing.T, repository gcStore) {
		ctx := context.Background()
		now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
		conversation, err := core.NewConversation("chat-open", "hh", "primary", "external-open", now)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.CreateConversation(ctx, conversation); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.AppendConversationMessage(ctx, core.ConversationMessage{
			ID: "message-1", ConversationID: "chat-open", Direction: core.MessageIncoming,
			Kind: core.MessageQuestionnaire, Status: core.MessageObserved, Text: "Готовы?",
			Options: []core.MessageOption{{ID: "1", Text: "Да"}}, OccurredAt: now,
		}, now); err != nil {
			t.Fatal(err)
		}
		open, err := repository.OpenQuestionnaireConversationIDs(ctx)
		if err != nil || len(open) != 1 || open[0] != "chat-open" {
			t.Fatalf("open=%#v err=%v", open, err)
		}
		if _, _, err := repository.AppendConversationMessage(ctx, core.ConversationMessage{
			ID: "message-2", ConversationID: "chat-open", Direction: core.MessageIncoming,
			Kind: core.MessageSystem, Status: core.MessageObserved,
			Text: "Событие переговоров HH (PARTICIPANT_LEFT)", OccurredAt: now.Add(time.Minute),
		}, now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		open, err = repository.OpenQuestionnaireConversationIDs(ctx)
		if err != nil || len(open) != 0 {
			t.Fatalf("open after left=%#v err=%v", open, err)
		}
	})
}

func TestConversationPurgeKeepsUnlinkedChats(t *testing.T) {
	gcStores(t, func(t *testing.T, repository gcStore) {
		ctx := context.Background()
		now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
		unlinked, err := core.NewConversation("chat-unlinked", "hh", "primary", "external-unlinked", now)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.CreateConversation(ctx, unlinked); err != nil {
			t.Fatal(err)
		}
		linked, err := core.NewConversation("chat-orphan", "hh", "primary", "external-orphan", now)
		if err != nil {
			t.Fatal(err)
		}
		linked.ApplicationID = "application-removed-long-ago"
		if _, _, err := repository.CreateConversation(ctx, linked); err != nil {
			t.Fatal(err)
		}

		removed, err := repository.PurgeOrphanConversations(ctx, "primary")
		if err != nil || removed != 1 {
			t.Fatalf("purged=%d err=%v", removed, err)
		}
		conversations, err := repository.ListConversations(ctx, storage.ConversationFilter{ProfileID: "primary"})
		if err != nil || len(conversations) != 1 || conversations[0].ID != "chat-unlinked" {
			t.Fatalf("conversations=%#v err=%v", conversations, err)
		}
	})
}

func TestAttachConversationApplicationLinksOnce(t *testing.T) {
	gcStores(t, func(t *testing.T, repository gcStore) {
		ctx := context.Background()
		now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
		conversation, err := core.NewConversation("chat-attach", "hh", "primary", "external-attach", now)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.CreateConversation(ctx, conversation); err != nil {
			t.Fatal(err)
		}
		attached, err := repository.AttachConversationApplication(ctx, "chat-attach", "application-1")
		if err != nil || !attached {
			t.Fatalf("attach=%v err=%v", attached, err)
		}
		again, err := repository.AttachConversationApplication(ctx, "chat-attach", "application-2")
		if err != nil || again {
			t.Fatalf("second attach=%v err=%v", again, err)
		}
		stored, err := repository.Conversation(ctx, "chat-attach")
		if err != nil || stored.ApplicationID != "application-1" {
			t.Fatalf("conversation=%#v err=%v", stored, err)
		}
	})
}

func TestApplicationGCRemovesStoredRejectionAfterWindow(t *testing.T) {
	gcStores(t, func(t *testing.T, repository gcStore) {
		ctx := context.Background()
		now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
		application := gcApplication(t, repository, "stored-rejection", "Sber", now.Add(-40*24*time.Hour))
		observedAt := now.Add(-38 * 24 * time.Hour)
		if err := repository.SaveApplicationPlatformState(ctx, core.ApplicationPlatformState{
			ApplicationID: application.ID, ExternalNegotiationID: "n-hidden", PlatformState: "discard",
			Disposition: core.ApplicationDispositionRejected, ObservedAt: observedAt,
		}); err != nil {
			t.Fatal(err)
		}
		tombstone, removed, err := repository.RemoveApplication(ctx, application.ID, core.ApplicationRemoval{
			Reason: core.ApplicationRemovalRetentionRejected, ObservedAt: observedAt, StaleBefore: now.Add(-30 * 24 * time.Hour),
		}, now)
		if err != nil || !removed || tombstone.Reason != core.ApplicationRemovalRetentionRejected {
			t.Fatalf("removed=%v tombstone=%#v err=%v", removed, tombstone, err)
		}
	})
}

func TestConversationReadMarkerRoundTrips(t *testing.T) {
	gcStores(t, func(t *testing.T, repository gcStore) {
		ctx := context.Background()
		now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
		conversation, err := core.NewConversation("chat-read", "hh", "primary", "external-read", now.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		conversation.UnreadCount = 2
		if _, _, err := repository.CreateConversation(ctx, conversation); err != nil {
			t.Fatal(err)
		}
		stored, err := repository.Conversation(ctx, "chat-read")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stored.MarkRead(now); err != nil {
			t.Fatal(err)
		}
		if err := repository.SaveConversation(ctx, stored, stored.Revision-1); err != nil {
			t.Fatalf("save read conversation: %v", err)
		}
		reloaded, err := repository.Conversation(ctx, "chat-read")
		if err != nil || reloaded.UnreadCount != 0 || reloaded.LastReadAt == nil {
			t.Fatalf("reloaded=%#v err=%v", reloaded, err)
		}
	})
}

func TestOpenQuestionnaireTracksBotPresenceWithoutOptions(t *testing.T) {
	gcStores(t, func(t *testing.T, repository gcStore) {
		ctx := context.Background()
		now := time.Date(2026, 9, 26, 13, 0, 0, 0, time.UTC)
		conversation, err := core.NewConversation("chat-bot", "hh", "primary", "external-bot", now)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.CreateConversation(ctx, conversation); err != nil {
			t.Fatal(err)
		}
		appendMessage := func(id, text string, kind core.MessageKind, at time.Time) {
			t.Helper()
			if _, _, err := repository.AppendConversationMessage(ctx, core.ConversationMessage{
				ID: core.MessageID(id), ConversationID: "chat-bot", Direction: core.MessageIncoming,
				Kind: kind, Status: core.MessageObserved, Text: text, OccurredAt: at,
			}, at); err != nil {
				t.Fatalf("append %s: %v", id, err)
			}
		}
		appendMessage("m-join", "Событие переговоров HH (PARTICIPANT_JOINED)", core.MessageSystem, now.Add(time.Minute))
		appendMessage("m-question", "Расскажите, пожалуйста, как долго вы занимаетесь тестированием?", core.MessageText, now.Add(2*time.Minute))
		open, err := repository.OpenQuestionnaireConversationIDs(ctx)
		if err != nil || len(open) != 1 || open[0] != "chat-bot" {
			t.Fatalf("open=%#v err=%v", open, err)
		}
		appendMessage("m-refusal", "К сожалению, сейчас мы не готовы пригласить вас на следующий этап.", core.MessageText, now.Add(3*time.Minute))
		open, err = repository.OpenQuestionnaireConversationIDs(ctx)
		if err != nil || len(open) != 0 {
			t.Fatalf("open after refusal=%#v err=%v", open, err)
		}
	})
}

func TestMarkConversationsReadLocallySweepsUnreadOnly(t *testing.T) {
	gcStores(t, func(t *testing.T, repository gcStore) {
		ctx := context.Background()
		now := time.Date(2026, 9, 26, 14, 0, 0, 0, time.UTC)
		for index, unread := range []int{2, 0, 1} {
			id := core.ConversationID("chat-sweep-" + string(rune('a'+index)))
			conversation, err := core.NewConversation(id, "hh", "primary", "external-"+string(id), now.Add(-time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			conversation.UnreadCount = unread
			if _, _, err := repository.CreateConversation(ctx, conversation); err != nil {
				t.Fatal(err)
			}
		}
		marked, err := repository.MarkConversationsReadLocally(ctx, "primary", now)
		if err != nil || marked != 2 {
			t.Fatalf("marked=%d err=%v", marked, err)
		}
		again, err := repository.MarkConversationsReadLocally(ctx, "primary", now.Add(time.Minute))
		if err != nil || again != 0 {
			t.Fatalf("second sweep marked=%d err=%v", again, err)
		}
		conversations, err := repository.ListConversations(ctx, storage.ConversationFilter{ProfileID: "primary"})
		if err != nil || len(conversations) != 3 {
			t.Fatalf("conversations=%#v err=%v", conversations, err)
		}
		for _, conversation := range conversations {
			if conversation.UnreadCount != 0 {
				t.Fatalf("conversation still unread: %#v", conversation)
			}
			// The already read chat is untouched by the sweep; the other two
			// carry the read marker.
			if conversation.ID != "chat-sweep-b" && conversation.LastReadAt == nil {
				t.Fatalf("conversation has no read marker: %#v", conversation)
			}
		}
	})
}
