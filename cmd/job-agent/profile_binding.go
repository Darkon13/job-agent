package main

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/adapters/hh"
	appconfig "github.com/Darkon13/job-agent/config"
	"github.com/Darkon13/job-agent/core"
	applicationoperator "github.com/Darkon13/job-agent/operator"
	taskworker "github.com/Darkon13/job-agent/worker"
)

// knownAnswerProfiles is the guarded set of profiles that may answer known
// questionnaires automatically: a config reload adds profiles while chat
// workers keep reading the set.
type knownAnswerProfiles struct {
	mu       sync.RWMutex
	profiles map[core.ProfileID]bool
}

func newKnownAnswerProfiles() *knownAnswerProfiles {
	return &knownAnswerProfiles{profiles: make(map[core.ProfileID]bool)}
}

func (set *knownAnswerProfiles) Add(profileID core.ProfileID) {
	if set == nil || profileID == "" {
		return
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	set.profiles[profileID] = true
}

func (set *knownAnswerProfiles) Has(profileID core.ProfileID) bool {
	set.mu.RLock()
	defer set.mu.RUnlock()
	return set.profiles[profileID]
}

func (set *knownAnswerProfiles) Len() int {
	set.mu.RLock()
	defer set.mu.RUnlock()
	return len(set.profiles)
}

// profileRuntimeBinder wires one enabled profile into the running service. It
// runs at startup for every declared profile and, with reload=true, from the
// config watcher when a fragment adds a profile to a live process. Reloads skip
// the fields that workers read through plain maps: those apply after the next
// restart, while guarded registries and maps are extended safely.
type profileRuntimeBinder struct {
	instances               map[string]adapter.Adapter
	profiles                map[core.ProfileID]profileRuntime
	contacts                map[core.ProfileID]applicationoperator.ApplicationProfileContext
	applicationModels       map[string]applicationoperator.ApplicationMessageModel
	employerMatcher         *applicationoperator.EmployerGroupMatcher
	answerResolver          taskworker.AnswerBlockResolver
	browserSubmissionDriver *hh.BrowserSubmissionDriver
	knownAnswers            *knownAnswerProfiles
	applicationPlans        *taskworker.LiveApplicationPlans

	profileStateReaders       map[core.ProfileID]adapter.ProfileStateReader
	profileStateWriters       *taskworker.ProfileStateWriterRegistry
	profileStatePlatforms     map[core.ProfileID]core.Platform
	conversationTransports    *taskworker.ConversationTransportRegistry
	applicationTransports     *taskworker.ApplicationTransportRegistry
	applicationStateObservers *taskworker.ApplicationStateObserverRegistry
	resumeTouchers            *taskworker.ResumeToucherRegistry
	resumePublishers          *taskworker.ResumePublisherRegistry
	testCapturers             *taskworker.VacancyTestCapturerRegistry
	testSubmitters            *taskworker.VacancyTestSubmitterRegistry
	qualificationReaders      *taskworker.QualificationCatalogRegistry
	qualificationAttempts     *taskworker.QualificationAttemptRegistry
	qualificationPlatforms    map[core.ProfileID]core.Platform
	qualificationAnswerModels map[core.ProfileID]taskworker.QualificationAnswerModel
	activityObservers         *taskworker.ProfileActivityObserverRegistry
	applicationTailoringPlans map[core.ProfileID]taskworker.ApplicationTailoringPlan
}

// bind registers the transports, readers and plans of one enabled profile.
func (binder *profileRuntimeBinder) bind(profile appconfig.Profile, preparer applicationoperator.ApplicationPreparer, reload bool) error {
	profileID := core.ProfileID(profile.Tag)
	if profile.Conversations.AnswerKnown {
		binder.knownAnswers.Add(profileID)
	}
	if profile.Answers != nil && profile.Answers.Model != nil {
		model, err := answerModel(profile.Answers.Model, binder.applicationModels)
		if err != nil {
			return fmt.Errorf("build answer model for profile %q: %w", profile.Tag, err)
		}
		if model != nil {
			if reload {
				// qualificationAnswerModels is read by the qualification worker
				// through a plain map, so a hot-added profile picks its model up
				// after the next restart.
				logf("profile %q answer model %q applies after the next restart", profile.Tag, model.Tag())
			} else {
				binder.qualificationAnswerModels[profileID] = model
				logf("profile %q answers unknown questions through model %q", profile.Tag, model.Tag())
			}
		}
	}
	runtime := binder.profiles[profileID]
	instance := binder.instances[profile.Adapter]
	apiReady := runtime.Status == core.ProfileEnabled && runtime.Reader != nil
	browserApplicationsReady := false
	browserConversationsReady := false
	var browserProfileStateWriter adapter.ProfileStateWriter
	if profile.StateFile != "" {
		if sessionBinder, ok := instance.(adapter.BrowserSessionBinder); ok {
			reader, err := sessionBinder.BindBrowserSession(profileID, profile.StateFile)
			if err != nil {
				return fmt.Errorf("bind browser session for profile %q: %w", profile.Tag, err)
			}
			runtime.BrowserReader = reader
			binder.profiles[profileID] = runtime
			logf("profile %q has a browser-backed read session", profile.Tag)
			if capturer, ok := instance.(adapter.VacancyTestCapturer); ok {
				if err := binder.testCapturers.Register(profileID, capturer); err != nil {
					return fmt.Errorf("register vacancy test capturer for profile %q: %w", profile.Tag, err)
				}
				logf("profile %q can capture vacancy tests through the browser session", profile.Tag)
			}
			if reader, ok := instance.(adapter.QualificationCatalogReader); ok {
				if err := binder.qualificationReaders.Register(profileID, reader); err != nil {
					return fmt.Errorf("register qualification catalog for profile %q: %w", profile.Tag, err)
				}
				if !reload {
					// Read through a plain map by the qualification API.
					binder.qualificationPlatforms[profileID] = core.Platform(instance.Name())
				}
				logf("profile %q can sync the skill verification catalog", profile.Tag)
			}
			if service, ok := instance.(adapter.QualificationAttemptService); ok && binder.answerResolver != nil {
				if err := binder.qualificationAttempts.Register(profileID, service); err != nil {
					return fmt.Errorf("register qualification attempt service for profile %q: %w", profile.Tag, err)
				}
			}
			if profileStateReader, ok := instance.(adapter.ProfileStateReader); ok {
				if !reload {
					// Read through a plain map by the profile state API.
					if !reload {
						// Read through a plain map by the profile state API.
						binder.profileStateReaders[profileID] = profileStateReader
					}
				}
			}
		}
		if sessionBinder, ok := instance.(adapter.BrowserProfileStateSessionBinder); ok {
			writer, err := sessionBinder.BindBrowserProfileStateSession(profileID, profile.StateFile)
			if err != nil {
				return fmt.Errorf("bind browser profile state session for profile %q: %w", profile.Tag, err)
			}
			browserProfileStateWriter = writer
		}
		if sessionBinder, ok := instance.(adapter.BrowserConversationSessionBinder); ok {
			transport, err := sessionBinder.BindBrowserConversationSession(profileID, profile.StateFile, adapter.BrowserConversationOptions{
				AllowSend: profile.Conversations.AllowSend, AllowMarkRead: profile.Conversations.AllowMarkRead,
			})
			if err != nil {
				return fmt.Errorf("bind browser conversation session for profile %q: %w", profile.Tag, err)
			}
			if err := binder.conversationTransports.Register(profileID, transport); err != nil {
				return fmt.Errorf("register browser conversation transport for profile %q: %w", profile.Tag, err)
			}
			browserConversationsReady = true
			logf("profile %q has a browser-backed conversation session", profile.Tag)
		}
		if !apiReady && profile.Applications.ExecutionMode() != appconfig.ApplicationModeDryRun {
			if sessionBinder, ok := instance.(adapter.BrowserApplicationSessionBinder); ok {
				transport, err := sessionBinder.BindBrowserApplicationSession(profileID, profile.StateFile, adapter.BrowserApplicationOptions{
					AllowVisibilityChange: profile.Applications.AllowVisibilityChange,
					ResumeID:              profile.Resume,
				})
				if err != nil {
					return fmt.Errorf("bind browser application session for profile %q: %w", profile.Tag, err)
				}
				if err := binder.applicationTransports.Register(profileID, transport); err != nil {
					return fmt.Errorf("register browser application transport for profile %q: %w", profile.Tag, err)
				}
				if submitter, ok := instance.(adapter.VacancyTestSubmitter); ok {
					if err := binder.testSubmitters.Register(profileID, submitter); err != nil {
						return fmt.Errorf("register vacancy test submitter for profile %q: %w", profile.Tag, err)
					}
				}
				browserApplicationsReady = true
				logf("profile %q uses explicit browser-backed application transport", profile.Tag)
			}
		}
	}
	if binder.browserSubmissionDriver != nil && instance.Name() == hh.Name && strings.TrimSpace(profile.StateFile) != "" {
		if err := binder.applicationTransports.RegisterBrowserSubmitter(profileID, binder.browserSubmissionDriver); err != nil {
			return fmt.Errorf("register browser submitter for profile %q: %w", profile.Tag, err)
		}
		logf("profile %q has the automatic browser submission fallback", profile.Tag)
	}
	if apiReady || runtime.BrowserReader != nil {
		if profileStateReader, ok := instance.(adapter.ProfileStateReader); ok {
			binder.profileStateReaders[profileID] = profileStateReader
		}
		writer, ok := instance.(adapter.ProfileStateWriter)
		if !ok {
			writer = browserProfileStateWriter
		}
		if writer != nil {
			if err := binder.profileStateWriters.Register(profileID, writer); err != nil {
				return fmt.Errorf("register profile state writer for profile %q: %w", profile.Tag, err)
			}
			if !reload {
				// Read through a plain map by the profile state workflows.
				binder.profileStatePlatforms[profileID] = core.Platform(instance.Name())
			}
		}
	}
	if apiReady {
		if transport, ok := instance.(adapter.ConversationTransport); ok && !browserConversationsReady {
			if err := binder.conversationTransports.Register(profileID, transport); err != nil {
				return fmt.Errorf("register conversation transport for profile %q: %w", profile.Tag, err)
			}
		}
		if transport, ok := instance.(adapter.ApplicationTransport); ok {
			if err := binder.applicationTransports.Register(profileID, transport); err != nil {
				return fmt.Errorf("register application transport for profile %q: %w", profile.Tag, err)
			}
		}
		if observer, ok := instance.(adapter.ApplicationStateObserver); ok {
			if err := binder.applicationStateObservers.Register(profileID, observer); err != nil {
				return fmt.Errorf("register application state observer for profile %q: %w", profile.Tag, err)
			}
		}
	} else if runtime.BrowserReader == nil {
		logf("profile %q has no authorized API session; API workers are disabled", profile.Tag)
	}
	if !binder.applicationStateObservers.Has(profileID) {
		if observer, ok := runtime.BrowserReader.(adapter.ApplicationStateObserver); ok {
			if err := binder.applicationStateObservers.Register(profileID, observer); err != nil {
				return fmt.Errorf("register browser application state observer for profile %q: %w", profile.Tag, err)
			}
		}
	}
	applicationReady := apiReady || browserApplicationsReady || runtime.BrowserReader != nil && profile.Applications.ExecutionMode() == appconfig.ApplicationModeDryRun
	if applicationReady {
		if !apiReady && !browserApplicationsReady {
			if err := binder.applicationTransports.RegisterVacancyReader(profileID, runtime.BrowserReader); err != nil {
				return fmt.Errorf("register browser vacancy reader for profile %q: %w", profile.Tag, err)
			}
		}
		if searcher, ok := instance.(adapter.VacancySearcher); ok {
			if err := binder.applicationTransports.RegisterVacancySearcher(profileID, searcher); err != nil {
				return fmt.Errorf("register vacancy searcher for profile %q: %w", profile.Tag, err)
			}
		}
		jitterMin, jitterMax, err := profile.Applications.SubmitJitterDurations(instance.Name())
		if err != nil {
			return fmt.Errorf("resolve application pacing for profile %q: %w", profile.Tag, err)
		}
		applicationPlan := taskworker.ApplicationPlan{
			ResumeID: profile.Resume, Mode: core.ApplicationExecutionMode(profile.Applications.ExecutionMode()),
			Message: profile.Applications.Message, Preparer: preparer,
			DailyLimit:      profile.Applications.EffectiveDailyLimit(instance.Name()),
			SubmitJitterMin: jitterMin, SubmitJitterMax: jitterMax,
			Timezone:       profile.Applications.LocationName(),
			SkipValidation: profile.Applications.SkipValidation(),
		}
		tailoringProcessor, tailoringPaths, tailoringErr := applicationTailoringProcessor(profile, binder.applicationModels, binder.contacts[profileID])
		if tailoringErr != nil {
			return fmt.Errorf("build application tailoring processor for profile %q: %w", profile.Tag, tailoringErr)
		}
		if tailoringProcessor != nil {
			if _, hasReader := binder.profileStateReaders[profileID]; !hasReader {
				return fmt.Errorf("profile %q application tailoring requires a profile state reader", profile.Tag)
			}
			if _, resolveErr := binder.profileStateWriters.Resolve(profileID); resolveErr != nil {
				return fmt.Errorf("profile %q application tailoring requires a profile state writer", profile.Tag)
			}
			tailoringPlan := taskworker.ApplicationTailoringPlan{
				Processor:       tailoringProcessor,
				AllowedPaths:    tailoringPaths,
				EmployerMatcher: binder.employerMatcher,
			}
			if !reload {
				// Read through a plain map by the tailoring handler; the plan
				// itself still carries the tailoring for submits.
				binder.applicationTailoringPlans[profileID] = tailoringPlan
			}
			applicationPlan.Tailoring = &tailoringPlan
		}
		binder.applicationPlans.Set(profileID, applicationPlan)
	}
	if toucher, ok := instance.(adapter.ResumeToucher); ok {
		if err := binder.resumeTouchers.Register(profileID, toucher); err != nil {
			return fmt.Errorf("register resume toucher for profile %q: %w", profile.Tag, err)
		}
	} else if instance.Name() == hh.Name && profile.StateFile != "" {
		if _, err := os.Stat(profile.StateFile); err == nil {
			toucher, err := hh.NewResumeTouchTransport(profile.StateFile, nil)
			if err != nil {
				return fmt.Errorf("create HH resume toucher for profile %q: %w", profile.Tag, err)
			}
			if err := binder.resumeTouchers.Register(profileID, toucher); err != nil {
				return fmt.Errorf("register HH resume toucher for profile %q: %w", profile.Tag, err)
			}
		}
	}
	if observer, ok := instance.(adapter.ProfileActivityObserver); ok {
		if err := binder.activityObservers.Register(profileID, observer); err != nil {
			return fmt.Errorf("register profile activity observer for profile %q: %w", profile.Tag, err)
		}
	} else if instance.Name() == hh.Name && profile.StateFile != "" {
		if _, err := os.Stat(profile.StateFile); err == nil {
			observer, err := hh.NewResumeTouchTransport(profile.StateFile, nil)
			if err != nil {
				return fmt.Errorf("create HH profile activity observer for profile %q: %w", profile.Tag, err)
			}
			if err := binder.activityObservers.Register(profileID, observer); err != nil {
				return fmt.Errorf("register HH profile activity observer for profile %q: %w", profile.Tag, err)
			}
		}
	}
	if apiReady {
		if publisher, ok := instance.(adapter.ResumePublisher); ok {
			if err := binder.resumePublishers.Register(profileID, publisher); err != nil {
				return fmt.Errorf("register resume publisher for profile %q: %w", profile.Tag, err)
			}
		}
	}
	return nil
}
