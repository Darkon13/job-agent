package hh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

var skillVerificationLinkPattern = regexp.MustCompile(`/applicant/skills/([^/?#]+)/verification_methods`)

var _ adapter.QualificationCatalogReader = (*Adapter)(nil)

func (a *Adapter) SyncQualifications(ctx context.Context, profileID core.ProfileID) ([]core.QualificationOffering, error) {
	a.mu.RLock()
	client := a.browserClients[profileID]
	a.mu.RUnlock()
	if client == nil {
		return nil, operationError(core.ErrorUnsupported, "skill_verifications.catalog", "HH profile has no browser read session", nil)
	}
	return client.SyncQualifications(ctx, profileID)
}

// SyncQualifications reads the skill verification methods page. It never
// starts an attempt; level tabs and kind cards are only described.
func (client *BrowserReadClient) SyncQualifications(ctx context.Context, profileID core.ProfileID) ([]core.QualificationOffering, error) {
	if profileID == "" || profileID != client.profileID {
		return nil, errors.New("HH browser qualification reader profile does not match")
	}
	endpoint := strings.TrimRight(client.webBaseURL, "/") + "/applicant/skill_verifications/methods"
	document, finalURL, err := client.getHTML(ctx, endpoint, "skill_verifications.catalog")
	if err != nil {
		return nil, err
	}
	if isLoginURL(finalURL) {
		return nil, operationError(core.ErrorUnauthorized, "skill_verifications.catalog", "HH browser session requires authentication", nil)
	}
	rendered, err := renderHTMLNode(document)
	if err != nil {
		return nil, operationError(core.ErrorTemporaryFailure, "skill_verifications.catalog", "HH catalog page could not be rendered", err)
	}
	return ParseQualificationCatalog(rendered, Name, profileID, time.Now().UTC())
}

type qualificationLevel struct {
	id    string
	name  string
	order *int
}

// ParseQualificationCatalog normalizes the methods page into offerings: one per
// skill, level and kind. The live page structure is confirmed by browser E2E;
// an unrecognized page is reported as unsupported instead of guessed.
func ParseQualificationCatalog(document []byte, platform core.Platform, profileID core.ProfileID, observedAt time.Time) ([]core.QualificationOffering, error) {
	if platform == "" || profileID == "" || observedAt.IsZero() {
		return nil, errors.New("qualification catalog parsing requires platform, profile and observed time")
	}
	root, err := html.Parse(bytes.NewReader(document))
	if err != nil {
		return nil, fmt.Errorf("parse skill verification catalog: %w", err)
	}
	var containers []*html.Node
	walkHTML(root, func(node *html.Node) bool {
		if htmlAttribute(node, "data-qa") == "skills-verification-method-container" {
			containers = append(containers, node)
		}
		return true
	})
	if len(containers) == 0 {
		return nil, operationError(core.ErrorUnsupported, "skill_verifications.catalog", "HH skill verification catalog page is not recognized", nil)
	}
	offerings := make([]core.QualificationOffering, 0, len(containers))
	for _, container := range containers {
		link := findHTMLNode(container, func(node *html.Node) bool {
			return node.Type == html.ElementNode && node.Data == "a" && skillVerificationLinkPattern.MatchString(htmlAttribute(node, "href"))
		})
		if link == nil {
			continue
		}
		match := skillVerificationLinkPattern.FindStringSubmatch(htmlAttribute(link, "href"))
		familyID := strings.TrimSpace(match[1])
		familyName := strings.TrimSpace(elementText(link))
		if familyName == "" {
			title := findHTMLNode(container, func(node *html.Node) bool {
				return htmlAttribute(node, "data-qa") == "verification-method-title"
			})
			familyName = strings.TrimSpace(elementText(title))
		}
		if familyID == "" || familyName == "" {
			continue
		}
		for _, level := range qualificationLevels(container) {
			for _, kind := range qualificationKinds(container) {
				externalID := familyID + ":" + level.id + ":" + kind
				offering := core.QualificationOffering{
					ID: core.QualificationID(string(platform) + ":" + externalID), Platform: platform,
					ProfileID: profileID, ExternalID: externalID,
					Status: core.QualificationAvailable, ObservedAt: observedAt,
					Qualification: core.QualificationDescriptor{
						FamilyID: familyID, FamilyName: familyName,
						LevelID: level.id, LevelName: level.name, LevelOrder: level.order,
					},
				}
				if err := offering.Validate(); err != nil {
					return nil, err
				}
				offerings = append(offerings, offering)
			}
		}
	}
	if len(offerings) == 0 {
		return nil, operationError(core.ErrorUnsupported, "skill_verifications.catalog", "HH skill verification catalog contains no observable levels", nil)
	}
	sort.Slice(offerings, func(i, j int) bool { return offerings[i].ID < offerings[j].ID })
	return offerings, nil
}

func qualificationLevels(container *html.Node) []qualificationLevel {
	var levels []qualificationLevel
	seen := make(map[string]struct{})
	walkHTML(container, func(node *html.Node) bool {
		if htmlAttribute(node, "data-qa") != "applicant-keyskills-verification-methods-level-tab" {
			return true
		}
		name := strings.TrimSpace(elementText(node))
		if name == "" {
			return true
		}
		id := qualificationLevelID(name)
		if _, exists := seen[id]; exists {
			return true
		}
		seen[id] = struct{}{}
		order := len(levels)
		levels = append(levels, qualificationLevel{id: id, name: name, order: &order})
		return true
	})
	if len(levels) == 0 {
		return []qualificationLevel{{id: "default", name: "По умолчанию"}}
	}
	return levels
}

func qualificationLevelID(label string) string {
	normalized := strings.ToLower(strings.TrimSpace(label))
	switch {
	case strings.Contains(normalized, "лёгк"), strings.Contains(normalized, "легк"),
		strings.Contains(normalized, "easy"), strings.Contains(normalized, "basic"):
		return "easy"
	case strings.Contains(normalized, "средн"), strings.Contains(normalized, "medium"):
		return "medium"
	case strings.Contains(normalized, "сложн"), strings.Contains(normalized, "hard"), strings.Contains(normalized, "advanced"):
		return "hard"
	}
	slug := strings.ReplaceAll(core.NormalizeQuestionText(label), " ", "-")
	if slug == "" {
		return "default"
	}
	return slug
}

func qualificationKinds(container *html.Node) []string {
	kinds := make([]string, 0, 2)
	seen := make(map[string]struct{})
	walkHTML(container, func(node *html.Node) bool {
		qa := htmlAttribute(node, "data-qa")
		kind := ""
		switch {
		case strings.HasSuffix(qa, "kind-card-theory"):
			kind = "theory"
		case strings.HasSuffix(qa, "kind-card-practice"):
			kind = "practice"
		}
		if kind == "" {
			return true
		}
		if _, exists := seen[kind]; exists {
			return true
		}
		seen[kind] = struct{}{}
		kinds = append(kinds, kind)
		return true
	})
	if len(kinds) == 0 {
		return []string{"theory"}
	}
	sort.Strings(kinds)
	return kinds
}

func elementText(node *html.Node) string {
	if node == nil {
		return ""
	}
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
			return
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return builder.String()
}
