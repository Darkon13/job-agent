package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/presenter"
)

// profileCatalogResume is one resume of the account as the backend reports it.
type profileCatalogResume struct {
	ID      string `json:"id"`
	Title   string `json:"title,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

// profileCatalogIdentity is the cached, non-secret account summary.
type profileCatalogIdentity struct {
	DisplayName string `json:"display_name,omitempty"`
	Email       string `json:"email,omitempty"`
	Phone       string `json:"phone,omitempty"`
	AccountHash string `json:"account_hash,omitempty"`
	CapturedAt  string `json:"captured_at,omitempty"`
}

// profileCatalogSession reports the browser session file of the profile.
type profileCatalogSession struct {
	StateFile  string     `json:"state_file,omitempty"`
	Present    bool       `json:"present"`
	ModifiedAt *time.Time `json:"modified_at,omitempty"`
}

// profileCatalogEntry is the operator-facing profile view: where it is
// declared, which account it is and whether it has a live session.
type profileCatalogEntry struct {
	Tag      string                  `json:"tag"`
	Adapter  string                  `json:"adapter"`
	Platform string                  `json:"platform"`
	Enabled  bool                    `json:"enabled"`
	Source   string                  `json:"source"`
	Identity *profileCatalogIdentity `json:"identity,omitempty"`
	Resumes  []profileCatalogResume  `json:"resumes"`
	Session  profileCatalogSession   `json:"session"`
}

func runProfile(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	if len(args) == 0 {
		return errors.New("usage: job-agent profile list|show|add [flags]")
	}
	switch args[0] {
	case "list":
		return runProfileList(ctx, args[1:], output, client)
	case "show":
		return runProfileShow(ctx, args[1:], output, client)
	case "add":
		return runProfileAdd(ctx, args[1:], output, client)
	default:
		return fmt.Errorf("unknown profile command %q", args[0])
	}
}

// profileDraftView mirrors the backend profile draft record.
type profileDraftView struct {
	Tag       string `json:"tag"`
	Platform  string `json:"platform"`
	Adapter   string `json:"adapter"`
	StateFile string `json:"state_file"`
	Identity  *struct {
		DisplayName string `json:"display_name,omitempty"`
		Email       string `json:"email,omitempty"`
		Phone       string `json:"phone,omitempty"`
		AccountHash string `json:"account_hash,omitempty"`
		CapturedAt  string `json:"captured_at,omitempty"`
	} `json:"identity,omitempty"`
	Resumes []profileCatalogResume `json:"resumes"`
	Status  string                 `json:"status"`
}

// runProfileAdd walks the onboarding flow from the terminal: it registers a
// draft, drives the same interactive login the dashboard uses and applies the
// profile fragment into the profile store.
func runProfileAdd(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	return runProfileAddWith(ctx, args, output, client, promptTTY)
}

func runProfileAddWith(ctx context.Context, args []string, output io.Writer, client *http.Client, prompt authPrompter) error {
	flags := flag.NewFlagSet("job-agent profile add", flag.ContinueOnError)
	flags.SetOutput(output)
	apiURL := flags.String("api", "http://127.0.0.1:8080", "job-agent backend URL")
	adapterTag := flags.String("adapter", "", "adapter tag (default: the first HH adapter)")
	primary := flags.String("primary", "", "platform resume id of the primary resume")
	imageProtocol := flags.String("image-protocol", "auto", "auto, kitty, sixel, unicode or file")
	tag := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		tag = strings.TrimSpace(args[0])
		args = args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if tag == "" {
		return errors.New("usage: job-agent profile add <tag> [--adapter tag] [--primary resume-id] [--api URL]")
	}
	protocol, err := presenter.ParseProtocol(*imageProtocol)
	if err != nil {
		return err
	}
	base, err := authBaseURL(*apiURL)
	if err != nil {
		return err
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	if err := checkAuthAPIVersion(ctx, client, base); err != nil {
		return err
	}
	var draft profileDraftView
	if err := authJSON(ctx, client, http.MethodPost, base+"/api/v1/profile-drafts",
		map[string]string{"tag": tag, "adapter": strings.TrimSpace(*adapterTag)}, &draft); err != nil {
		return fmt.Errorf("create profile draft: %w", err)
	}
	fmt.Fprintf(output, "DRAFT tag=%s state=%s\n", draft.Tag, draft.StateFile)
	if _, err := loginAuthSession(ctx, client, base, authSessionRequest{
		Platform: draft.Platform, ProfileID: draft.Tag,
	}, protocol, prompt, output); err != nil {
		return err
	}
	// The post-login capture runs in the background, so refresh explicitly to
	// show the account before choosing the primary resume.
	if err := authJSON(ctx, client, http.MethodPost, base+"/api/v1/profile-drafts/"+url.PathEscape(tag)+"/refresh", nil, &draft); err != nil {
		return fmt.Errorf("capture profile identity: %w", err)
	}
	printProfileDraft(output, draft)
	primaryID := strings.TrimSpace(*primary)
	if primaryID == "" && len(draft.Resumes) > 1 {
		for index, resume := range draft.Resumes {
			label := resume.ID
			if resume.Title != "" {
				label = resume.Title + " (" + resume.ID + ")"
			}
			fmt.Fprintf(output, "  %d) %s\n", index+1, label)
		}
		value, err := prompt("Номер основного резюме: ")
		if err != nil {
			return err
		}
		index, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || index < 1 || index > len(draft.Resumes) {
			return errors.New("primary resume must be one of the listed numbers")
		}
		primaryID = draft.Resumes[index-1].ID
	}
	if err := authJSON(ctx, client, http.MethodPost, base+"/api/v1/profile-drafts/"+url.PathEscape(tag)+"/apply",
		map[string]string{"primary_resume": primaryID}, &draft); err != nil {
		return fmt.Errorf("apply profile draft: %w", err)
	}
	fmt.Fprintf(output, "APPLIED tag=%s status=%s; профиль появится после перезапуска backend\n", draft.Tag, draft.Status)
	return nil
}

func printProfileDraft(output io.Writer, draft profileDraftView) {
	if draft.Identity != nil {
		parts := make([]string, 0, 4)
		for _, value := range []string{draft.Identity.DisplayName, draft.Identity.Email, draft.Identity.Phone} {
			if value = strings.TrimSpace(value); value != "" {
				parts = append(parts, value)
			}
		}
		if hash := strings.TrimSpace(draft.Identity.AccountHash); hash != "" {
			parts = append(parts, "аккаунт "+hash)
		}
		if len(parts) != 0 {
			fmt.Fprintf(output, "IDENTITY %s\n", strings.Join(parts, " · "))
		}
	}
	if len(draft.Resumes) == 0 {
		fmt.Fprintln(output, "RESUMES нет")
		return
	}
	for _, resume := range draft.Resumes {
		label := resume.ID
		if resume.Title != "" {
			label = resume.Title + " (" + resume.ID + ")"
		}
		fmt.Fprintf(output, "RESUME %s\n", label)
	}
}

func runProfileList(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	flags := flag.NewFlagSet("job-agent profile list", flag.ContinueOnError)
	flags.SetOutput(output)
	apiURL := flags.String("api", "http://127.0.0.1:8080", "job-agent backend URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	entries, err := fetchProfileCatalog(ctx, *apiURL, client)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintln(output, "профили не объявлены")
		return nil
	}
	for index, entry := range entries {
		if index != 0 {
			fmt.Fprintln(output)
		}
		printProfileEntry(output, entry)
	}
	return nil
}

func runProfileShow(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	flags := flag.NewFlagSet("job-agent profile show", flag.ContinueOnError)
	flags.SetOutput(output)
	apiURL := flags.String("api", "http://127.0.0.1:8080", "job-agent backend URL")
	// The flag package stops at the first positional argument, so a leading tag
	// is taken out before parsing to accept both `show <tag> --api URL` and
	// `show --api URL <tag>`.
	tag := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		tag = strings.TrimSpace(args[0])
		args = args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if tag == "" {
		tag = strings.TrimSpace(flags.Arg(0))
	}
	if tag == "" {
		return errors.New("usage: job-agent profile show <tag> [--api URL]")
	}
	entries, err := fetchProfileCatalog(ctx, *apiURL, client)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Tag == tag {
			printProfileEntry(output, entry)
			return nil
		}
	}
	return fmt.Errorf("profile %q is not declared", tag)
}

func fetchProfileCatalog(ctx context.Context, apiURL string, client *http.Client) ([]profileCatalogEntry, error) {
	base, err := authBaseURL(apiURL)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/profiles", nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("profile list failed: %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	var listing struct {
		Items []profileCatalogEntry `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&listing); err != nil {
		return nil, fmt.Errorf("decode profile list: %w", err)
	}
	return listing.Items, nil
}

func printProfileEntry(output io.Writer, entry profileCatalogEntry) {
	state := "включён"
	if !entry.Enabled {
		state = "выключен"
	}
	source := entry.Source
	if source == "" {
		source = "config"
	}
	fmt.Fprintf(output, "%s (%s, %s, источник: %s)\n", entry.Tag, profilePlatformLabel(entry), state, source)
	if identity := profileIdentityLabel(entry); identity != "" {
		fmt.Fprintf(output, "    %s\n", identity)
	}
	fmt.Fprintf(output, "    резюме: %s\n", profileResumesLabel(entry.Resumes))
	fmt.Fprintf(output, "    сессия: %s\n", profileSessionLabel(entry.Session))
}

func profilePlatformLabel(entry profileCatalogEntry) string {
	switch {
	case entry.Adapter != "" && entry.Platform != "" && entry.Adapter != entry.Platform:
		return entry.Adapter + "/" + entry.Platform
	case entry.Platform != "":
		return entry.Platform
	case entry.Adapter != "":
		return entry.Adapter
	default:
		return "адаптер не задан"
	}
}

func profileIdentityLabel(entry profileCatalogEntry) string {
	if entry.Identity == nil {
		return ""
	}
	parts := make([]string, 0, 4)
	for _, value := range []string{entry.Identity.DisplayName, entry.Identity.Email, entry.Identity.Phone} {
		if value = strings.TrimSpace(value); value != "" {
			parts = append(parts, value)
		}
	}
	if hash := strings.TrimSpace(entry.Identity.AccountHash); hash != "" {
		parts = append(parts, "аккаунт "+hash)
	}
	return strings.Join(parts, " · ")
}

func profileResumesLabel(resumes []profileCatalogResume) string {
	if len(resumes) == 0 {
		return "нет"
	}
	parts := make([]string, 0, len(resumes))
	for _, resume := range resumes {
		label := resume.ID
		if title := strings.TrimSpace(resume.Title); title != "" {
			label = title + " (" + resume.ID + ")"
		}
		if resume.Primary {
			label += " — основное"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, ", ")
}

func profileSessionLabel(session profileCatalogSession) string {
	if !session.Present {
		return "нет"
	}
	if session.ModifiedAt == nil {
		return "есть"
	}
	return "есть, обновлена " + session.ModifiedAt.Local().Format(time.RFC3339)
}
