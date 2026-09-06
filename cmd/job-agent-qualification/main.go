package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Darkon13/job-agent/adapters/hh"
	appconfig "github.com/Darkon13/job-agent/config"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
	"github.com/Darkon13/job-agent/workflow"
)

var errReviewStopped = errors.New("review stopped by user")

type qualificationBrowser interface {
	OpenOffering(context.Context, string, string, string) (hh.QualificationOffering, error)
	StartAttempt(context.Context) (hh.QualificationCapture, error)
	Select(context.Context, string, []string) (hh.QualificationCapture, error)
	Next(context.Context, string, []string) (hh.QualificationCapture, error)
}

type qualificationStore interface {
	storage.TestCatalogRepository
	storage.ReviewRepository
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output, errorOutput io.Writer) error {
	flags := flag.NewFlagSet("job-agent-qualification", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	configPath := flags.String("config", "./data/config.local.json", "job-agent config")
	profileTag := flags.String("profile", "primary", "authenticated HH profile")
	skillID := flags.String("skill", "", "numeric HH skill id")
	level := flags.String("level", "Базовый", "qualification level tab")
	kind := flags.String("kind", "theory", "theory or practice")
	nodePath := flags.String("node", "/etc/profiles/per-user/darkon/bin/node", "Node.js executable")
	workerPath := flags.String("worker", "./browser-worker/hh-qualification.mjs", "Playwright worker")
	browserPath := flags.String("browser", "/etc/profiles/per-user/darkon/bin/google-chrome-stable", "Chromium executable")
	proxy := flags.String("proxy", "", "optional Playwright proxy, for example socks5://127.0.0.1:1080")
	headless := flags.Bool("headless", false, "run browser without a visible window")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*skillID) == "" {
		return errors.New("usage: job-agent-qualification -skill ID [-level Базовый] [-kind theory]")
	}
	if *kind != "theory" && *kind != "practice" {
		return errors.New("kind must be theory or practice")
	}
	cfg, err := appconfig.Load(*configPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	profile, err := hhProfile(cfg, *profileTag)
	if err != nil {
		return err
	}
	store, err := storesqlite.Open(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer store.Close()
	fmt.Fprintln(output, "[1/3] Запускаю изолированный Chrome с сохранённой HH-сессией…")
	browser, err := hh.StartQualificationBrowser(ctx, hh.QualificationBrowserOptions{
		NodePath: *nodePath, WorkerPath: *workerPath, BrowserPath: *browserPath,
		StateFile: profile.StateFile, Proxy: *proxy, Headless: *headless, Stderr: errorOutput,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := browser.Close(); err != nil && ctx.Err() == nil {
			fmt.Fprintf(errorOutput, "browser close: %v\n", err)
		}
	}()
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	openedAt := time.Now()
	fmt.Fprintln(output, "[2/3] Открываю карточку навыка и проверяю доступность уровня…")
	offering, err := browser.OpenOffering(ctx, *skillID, *level, *kind)
	if err != nil {
		return fmt.Errorf("open qualification: %w", err)
	}
	fmt.Fprintf(output, "[3/3] Карточка готова за %s. До START серверная попытка не создаётся.\n", time.Since(openedAt).Round(time.Second))
	printOffering(output, offering)
	fmt.Fprint(output, "Введите START, чтобы создать реальную таймированную попытку: ")
	line, ok := scanLine(scanner)
	if !ok || line != "START" {
		fmt.Fprintln(output, "Старт отменён; попытка не создавалась.")
		return scanner.Err()
	}
	result, err := reviewAttempt(ctx, store, browser, workflow.SystemClock{}, workflow.RandomIDGenerator{},
		core.ProfileID(profile.Tag), offering, scanner, output)
	if errors.Is(err, errReviewStopped) {
		fmt.Fprintf(output, "Остановлено без нажатия «Далее». Сессия %s сохранена для аудита.\n", result.SessionID)
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Готово: сессия=%s, подтверждено ответов=%d, известных вариантов вопросов=%d.\n",
		result.SessionID, result.Answers, result.KnownQuestions)
	return nil
}

type reviewResult struct {
	SessionID      core.ReviewSessionID
	Answers        int
	KnownQuestions int
}

func reviewAttempt(
	ctx context.Context,
	store qualificationStore,
	browser qualificationBrowser,
	clock workflow.Clock,
	ids workflow.IDGenerator,
	profileID core.ProfileID,
	offering hh.QualificationOffering,
	input *bufio.Scanner,
	output io.Writer,
) (reviewResult, error) {
	now := clock.Now()
	familyName := strings.TrimSpace(offering.FamilyName)
	if familyName == "" {
		familyName = "HH skill " + offering.SkillID
	}
	qualification := &core.QualificationDescriptor{
		FamilyID: "skill:" + offering.SkillID, FamilyName: familyName,
		LevelID: "label:" + core.NormalizeQuestionText(offering.Level), LevelName: offering.Level,
	}
	externalID := "skill:" + offering.SkillID + ":" + offering.Kind
	definition, err := core.NewProgressiveTestDefinition("hh", externalID,
		fmt.Sprintf("%s — %s (%s)", familyName, offering.Level, offering.Kind), qualification, now)
	if err != nil {
		return reviewResult{}, err
	}
	if _, err := store.UpsertTestDefinition(ctx, definition); err != nil {
		return reviewResult{}, fmt.Errorf("store qualification definition: %w", err)
	}
	definition, err = store.TestDefinition(ctx, definition.ID)
	if err != nil {
		return reviewResult{}, fmt.Errorf("load qualification definition: %w", err)
	}
	previousChoices, err := store.ReviewChoices(ctx, definition.ID)
	if err != nil {
		return reviewResult{}, fmt.Errorf("load previous review choices: %w", err)
	}
	var previousBlock *core.AnswerBlock
	if len(previousChoices) > 0 {
		previousBlock = &core.AnswerBlock{
			Tag: "local-review-history", Name: "Previously confirmed choices",
			Kind: core.AnswerBlockQualification, Platform: definition.Platform,
			Qualification: definition.Qualification, Answers: previousChoices,
		}
	}
	reviewID, err := ids.NewID("review")
	if err != nil {
		return reviewResult{}, err
	}
	correlationID, err := ids.NewID("correlation")
	if err != nil {
		return reviewResult{}, err
	}
	session, err := core.NewReviewSession(core.ReviewSessionID(reviewID), definition, profileID, core.CorrelationID(correlationID), now)
	if err != nil {
		return reviewResult{}, err
	}
	result := reviewResult{SessionID: session.ID}
	if _, err := store.CreateReviewSession(ctx, session); err != nil {
		return result, fmt.Errorf("create review session: %w", err)
	}
	fmt.Fprintf(output, "Сессия %s сохранена. Прохожу два информационных экрана HH и запускаю таймер…\n", session.ID)
	capture, err := browser.StartAttempt(ctx)
	if err != nil {
		return result, fmt.Errorf("start qualification attempt: %w", err)
	}
	fmt.Fprintln(output, "Попытка открыта; первый вопрос получен.")
	var observed []string
	for !capture.Completed() {
		if capture.Status != "question" {
			return result, fmt.Errorf("unsupported qualification capture status %q", capture.Status)
		}
		observedAt := clock.Now()
		fingerprint, _, err := definition.ObserveQuestion(capture.Question, observedAt)
		if err != nil {
			return result, fmt.Errorf("observe question: %w", err)
		}
		observed = append(observed, fingerprint)
		if _, err := store.UpsertTestDefinition(ctx, definition); err != nil {
			return result, fmt.Errorf("store observed question: %w", err)
		}
		promptID, err := ids.NewID("prompt")
		if err != nil {
			return result, err
		}
		prompt := core.ReviewPrompt{
			ID: core.ReviewPromptID(promptID), SessionID: session.ID, Revision: session.Revision,
			Question: capture.Question, CreatedAt: observedAt,
		}
		if capture.TimeLeftSeconds > 0 {
			deadline := observedAt.Add(time.Duration(capture.TimeLeftSeconds) * time.Second)
			prompt.Deadline = &deadline
		}
		expectedRevision := session.Revision
		if err := session.WaitForAnswer(prompt, observedAt); err != nil {
			return result, err
		}
		if err := store.SaveReviewPrompt(ctx, session, prompt, expectedRevision); err != nil {
			return result, fmt.Errorf("store review prompt: %w", err)
		}

		optionIDs, optionTexts, err := reviewQuestion(ctx, browser, capture, previousBlock, input, output)
		if err != nil {
			return result, err
		}
		selectedAt := clock.Now()
		selection, err := session.RecordSelection(prompt, optionTexts, "", "cli:user", expectedRevision, selectedAt)
		if err != nil {
			return result, err
		}
		// Persist the user's confirmed choice before the externally visible Next.
		if err := store.AppendReviewSelection(ctx, session, selection, expectedRevision); err != nil {
			return result, fmt.Errorf("store confirmed selection: %w", err)
		}
		result.Answers++
		capture, err = browser.Next(ctx, prompt.Question.Text, optionIDs)
		if err != nil {
			return result, fmt.Errorf("answer was recorded, but HH outcome is ambiguous: %w", err)
		}
	}
	completedAt := clock.Now()
	if _, err := definition.CompleteObservedAttempt(observed, completedAt); err != nil {
		return result, err
	}
	if _, err := store.UpsertTestDefinition(ctx, definition); err != nil {
		return result, fmt.Errorf("store completed attempt: %w", err)
	}
	if err := session.Complete(completedAt); err != nil {
		return result, err
	}
	if err := store.FinishReviewSession(ctx, session, session.Revision); err != nil {
		return result, fmt.Errorf("finish review session: %w", err)
	}
	result.KnownQuestions = len(definition.Questions)
	return result, nil
}

func reviewQuestion(ctx context.Context, browser qualificationBrowser, capture hh.QualificationCapture, previous *core.AnswerBlock, input *bufio.Scanner, output io.Writer) ([]string, []string, error) {
	printQuestion(output, capture)
	if previous != nil {
		resolved, err := core.ResolveQuestionAnswer(capture.Question, *previous)
		if err == nil {
			optionTexts, err := selectedOptionTexts(capture.Question, resolved.SelectedOptionIDs)
			if err != nil {
				return nil, nil, err
			}
			marked, err := browser.Select(ctx, capture.Question.Text, resolved.SelectedOptionIDs)
			if err != nil {
				return nil, nil, fmt.Errorf("mark previous browser choice: %w", err)
			}
			if !sameStrings(marked.SelectedOptionIDs, resolved.SelectedOptionIDs) {
				return nil, nil, errors.New("browser did not retain the previous option marks")
			}
			fmt.Fprintf(output, "Точное совпадение с прошлым подтверждённым выбором: %s\n", strings.Join(optionTexts, " | "))
			for {
				fmt.Fprint(output, "Enter — записать выбор и нажать «Далее»; r — выбрать вручную; q — остановиться: ")
				confirmation, ok := scanLine(input)
				if !ok {
					if err := input.Err(); err != nil {
						return nil, nil, err
					}
					return nil, nil, errReviewStopped
				}
				switch strings.ToLower(confirmation) {
				case "":
					return resolved.SelectedOptionIDs, optionTexts, nil
				case "r":
					goto manual
				case "q":
					return nil, nil, errReviewStopped
				default:
					fmt.Fprintln(output, "«Далее» не нажато: используйте Enter, r или q.")
				}
			}
		}
	}

manual:
	for {
		fmt.Fprint(output, "Выбор (например 2 или 1,3; q — остановиться): ")
		line, ok := scanLine(input)
		if !ok {
			if err := input.Err(); err != nil {
				return nil, nil, err
			}
			return nil, nil, errReviewStopped
		}
		if strings.EqualFold(line, "q") {
			return nil, nil, errReviewStopped
		}
		indexes, err := parseOptionIndexes(line, len(capture.Question.Options), capture.Question.Kind)
		if err != nil {
			fmt.Fprintf(output, "Некорректный выбор: %v\n", err)
			continue
		}
		optionIDs := make([]string, 0, len(indexes))
		optionTexts := make([]string, 0, len(indexes))
		for _, index := range indexes {
			option := capture.Question.Options[index-1]
			optionIDs = append(optionIDs, option.ID)
			optionTexts = append(optionTexts, option.Text)
		}
		marked, err := browser.Select(ctx, capture.Question.Text, optionIDs)
		if err != nil {
			return nil, nil, fmt.Errorf("mark browser options: %w", err)
		}
		if !sameStrings(marked.SelectedOptionIDs, optionIDs) {
			return nil, nil, errors.New("browser did not retain the requested option marks")
		}
		fmt.Fprintf(output, "Отмечено: %s\n", strings.Join(optionTexts, " | "))
		fmt.Fprint(output, "Enter — записать выбор и нажать «Далее»; r — перевыбрать; q — остановиться: ")
		confirmation, ok := scanLine(input)
		if !ok {
			if err := input.Err(); err != nil {
				return nil, nil, err
			}
			return nil, nil, errReviewStopped
		}
		switch strings.ToLower(confirmation) {
		case "":
			return optionIDs, optionTexts, nil
		case "r":
			continue
		case "q":
			return nil, nil, errReviewStopped
		default:
			fmt.Fprintln(output, "«Далее» не нажато: используйте Enter, r или q.")
		}
	}
}

func selectedOptionTexts(question core.Question, optionIDs []string) ([]string, error) {
	byID := make(map[string]string, len(question.Options))
	for _, option := range question.Options {
		byID[option.ID] = option.Text
	}
	texts := make([]string, 0, len(optionIDs))
	for _, optionID := range optionIDs {
		text, exists := byID[optionID]
		if !exists {
			return nil, fmt.Errorf("question has no resolved runtime option %q", optionID)
		}
		texts = append(texts, text)
	}
	return texts, nil
}

func parseOptionIndexes(value string, optionCount int, kind core.QuestionKind) ([]int, error) {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
	if len(parts) == 0 {
		return nil, errors.New("укажите номер варианта")
	}
	if kind == core.QuestionSingle && len(parts) != 1 {
		return nil, errors.New("здесь можно выбрать ровно один вариант")
	}
	if kind != core.QuestionSingle && kind != core.QuestionMultiple {
		return nil, fmt.Errorf("тип вопроса %q пока не поддержан", kind)
	}
	seen := make(map[int]struct{}, len(parts))
	indexes := make([]int, 0, len(parts))
	for _, part := range parts {
		index, err := strconv.Atoi(part)
		if err != nil || index < 1 || index > optionCount {
			return nil, fmt.Errorf("номер %q вне диапазона 1..%d", part, optionCount)
		}
		if _, exists := seen[index]; exists {
			continue
		}
		seen[index] = struct{}{}
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	return indexes, nil
}

func hhProfile(cfg appconfig.Config, tag string) (appconfig.Profile, error) {
	var profile appconfig.Profile
	for _, candidate := range cfg.Profiles {
		if candidate.Tag == tag {
			profile = candidate
			break
		}
	}
	if profile.Tag == "" || !profile.Enabled || strings.TrimSpace(profile.StateFile) == "" {
		return appconfig.Profile{}, fmt.Errorf("enabled profile %q with browser state is required", tag)
	}
	for _, adapter := range cfg.Adapters {
		if adapter.Tag == profile.Adapter {
			if adapter.Type != hh.Name {
				return appconfig.Profile{}, fmt.Errorf("profile %q does not use the HH adapter", tag)
			}
			return profile, nil
		}
	}
	return appconfig.Profile{}, fmt.Errorf("profile %q references an unknown adapter", tag)
}

func printOffering(output io.Writer, offering hh.QualificationOffering) {
	fmt.Fprintf(output, "HH skill=%s, уровень=%s, тип=%s\n", offering.SkillID, offering.Level, offering.Kind)
	if offering.Summary != "" {
		fmt.Fprintf(output, "%s\n", offering.Summary)
	}
	if !offering.StartAvailable {
		fmt.Fprintln(output, "Предупреждение: кнопка старта сейчас недоступна.")
	}
}

func printQuestion(output io.Writer, capture hh.QualificationCapture) {
	if capture.Progress.Total > 0 {
		fmt.Fprintf(output, "\n[%d/%d] %s\n", capture.Progress.Current, capture.Progress.Total, capture.Question.Text)
	} else {
		fmt.Fprintf(output, "\n%s\n", capture.Question.Text)
	}
	for index, option := range capture.Question.Options {
		fmt.Fprintf(output, "  %d. %s\n", index+1, option.Text)
	}
}

func scanLine(scanner *bufio.Scanner) (string, bool) {
	if !scanner.Scan() {
		return "", false
	}
	return strings.TrimSpace(scanner.Text()), true
}

func sameStrings(left, right []string) bool {
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	return len(left) == len(right) && strings.Join(left, "\x00") == strings.Join(right, "\x00")
}
