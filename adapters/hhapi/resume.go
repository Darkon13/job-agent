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
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Description         string   `json:"description"`
	AlternateURL        string   `json:"alternate_url"`
	PublishedAt         string   `json:"published_at"`
	Archived            bool     `json:"archived"`
	HasTest             bool     `json:"has_test"`
	ResponseLetter      bool     `json:"response_letter_required"`
	ClosedForApplicants bool     `json:"closed_for_applicants"`
	AllowMessages       bool     `json:"allow_messages"`
	Relations           []string `json:"relations"`
	WorkFormat          []struct {
		ID string `json:"id"`
	} `json:"work_format"`
	Salary            *Salary `json:"salary"`
	ProfessionalRoles []struct {
		ID string `json:"id"`
	} `json:"professional_roles"`
	Employer struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"employer"`
	KeySkills []struct {
		Name string `json:"name"`
	} `json:"key_skills"`
}

// BlockedRole отсекает роли вне профиля (фронтенд, PHP, ML/Data и т.п.).
func BlockedRoleName(name string) bool { return Vacancy{Name: name}.BlockedRole() }

func BlockedCompanyName(name string) bool {
	v := Vacancy{}
	v.Employer.Name = name
	return v.BlockedCompany()
}

func (v Vacancy) BlockedRole() bool {
	name := " " + strings.ToLower(v.Name) + " "
	for _, term := range []string{
		"frontend", "front-end", "фронтенд", "react", "vue", "angular", "next.js", "nuxt",
		"php", "laravel", "symfony", "bitrix", "битрикс", "wordpress",
		"аналитик", "analyst", "data scientist", "data engineer", "data инженер", "data-инженер",
		"machine learning", " ml ", "ml-", "mlops", "нейросет", "нейронн", "computer vision", "nlp", "llm",
		"c++", "с++", "embedded", "микроконтроллер", "firmware", "прошивк", "электронн", "fpga", "verilog",
		"продаж", "sales", "call center", "колл-центр", "рекрутер", "recruiter", "менеджер по продажам",
		"руководитель", "директор", "лидер", "менеджер", "ассистент", "секретарь", "врач",
		"видеомонтаж", "маркетолог", "юрист", "бухгалтер", "лицензи", "офис-менеджер",
	} {
		if strings.Contains(name, term) {
			return true
		}
	}
	return false
}

// BlockedCompany исключает нежелательных работодателей.
func (v Vacancy) BlockedCompany() bool {
	name := strings.ToLower(v.Employer.Name)
	for _, blocked := range []string{"сбер", "sber", "алабуга", "alabuga", "контур", "kontur", "aston"} {
		if strings.Contains(name, blocked) {
			return true
		}
	}
	return false
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

var allowedRoleTerms = []string{
	"поддерж", "support", "helpdesk", "service desk", "devops", "sre", "backend", "бэкенд",
	"разработчик", "developer", "программист", "администратор", "sysadmin", "системный",
	"инфраструктур", "infrastructure", "network engineer", "сетевой инженер", "тестировщ", "qa", "sdet",
	"1с", "1c", "инженер", ".net", "c#", "python", "golang", "go разработчик",
}

// AllowedRoleName отсекает роли вне целевого профиля.
func AllowedRoleName(name string) bool {
	text := strings.ToLower(name)
	for _, term := range allowedRoleTerms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}
