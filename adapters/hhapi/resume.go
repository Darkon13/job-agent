package hhapi

import (
	"context"
	"strings"
)

type Resume struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Skills   string   `json:"skills"`
	SkillSet []string `json:"skill_set"`
}

type ResumePatch struct {
	Skills   *string   `json:"skills,omitempty"`
	SkillSet *[]string `json:"skill_set,omitempty"`
}

type Vacancy struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	WorkFormat  []struct {
		ID string `json:"id"`
	} `json:"work_format"`
	KeySkills []struct {
		Name string `json:"name"`
	} `json:"key_skills"`
}

// Remote сообщает, что вакансия допускает удалённую работу.
func (v Vacancy) Remote() bool {
	for _, format := range v.WorkFormat {
		if format.ID == "REMOTE" {
			return true
		}
	}
	text := strings.ToLower(v.Description)
	for _, marker := range []string{"удален", "удалён", "remote", "из любого города", "дистанцион"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (c *Client) Resume(ctx context.Context, id string) (Resume, error) {
	var resume Resume
	err := c.Request(ctx, "GET", "/resumes/"+id, nil, &resume)
	return resume, err
}

func (c *Client) Vacancy(ctx context.Context, id string) (Vacancy, error) {
	var vacancy Vacancy
	err := c.Request(ctx, "GET", "/vacancies/"+id, nil, &vacancy)
	return vacancy, err
}

func (c *Client) UpdateResume(ctx context.Context, id string, patch ResumePatch) error {
	return c.Request(ctx, "PUT", "/resumes/"+id, patch, nil)
}

// Tailor добавляет key_skills вакансии в резюме и возвращает функцию восстановления.
func (c *Client) Tailor(ctx context.Context, resumeID, vacancyID string) (func(), error) {
	baseline, err := c.Resume(ctx, resumeID)
	if err != nil {
		return func() {}, err
	}
	vacancy, err := c.Vacancy(ctx, vacancyID)
	if err != nil {
		return func() {}, err
	}
	seen := map[string]bool{}
	var tags []string
	for _, skill := range vacancy.KeySkills {
		name := strings.TrimSpace(skill.Name)
		if name != "" && !seen[strings.ToLower(name)] {
			seen[strings.ToLower(name)] = true
			tags = append(tags, name)
		}
	}
	for _, name := range baseline.SkillSet {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		tags = append(tags, name)
	}
	if len(tags) > 30 {
		tags = tags[:30]
	}
	text := baseline.Skills
	if len(vacancy.KeySkills) > 0 {
		var names []string
		for _, skill := range vacancy.KeySkills {
			names = append(names, skill.Name)
		}
		text = strings.TrimRight(text, " \n") + "\n\nПод вакансию «" + vacancy.Name + "»: " + strings.Join(names, ", ")
	}
	if err := c.UpdateResume(ctx, resumeID, ResumePatch{Skills: &text, SkillSet: &tags}); err != nil {
		return func() {}, err
	}
	restore := func() {
		_ = c.UpdateResume(ctx, resumeID, ResumePatch{Skills: &baseline.Skills, SkillSet: &baseline.SkillSet})
	}
	return restore, nil
}
