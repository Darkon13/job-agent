# HH: resume/profile contract

Целевая реализация использует актуальную resume-profile схему HH, а не повторяет
HTML-формы вручную. Источники: локальный OpenAPI snapshot и browser observation
2026-07-18.

## API-first endpoints

| Операция | Endpoint |
|---|---|
| Список своих резюме | `GET /resumes/mine` |
| Полное резюме | `GET /resumes/{resume_id}` |
| Схема визарда | `GET /resume_profile/{resume_id}` |
| Создание | `POST /resume_profile` |
| Обновление | `PUT /resume_profile/{resume_id}` |
| Словари | `GET /resume_profile/dictionaries` |
| Проверка лимита создания | `GET /resumes/creation_availability` |
| Публикация/поднятие | `POST /resumes/{resume_id}/publish` |

Старые `POST /resumes` и `PUT /resumes/{resume_id}` помечены deprecated и нужны
только как fallback совместимости.

Create profile принимает entry context (`default`, `vacancy_response`, варианты
onboarding), optional `vacancy_id`, координаты, `clone_resume_id`,
`update_profile` и дополнительные wizard properties.

Update body разделяет:

```json
{
  "current_screen_id": "...",
  "profile": {},
  "resume": {},
  "creds": {},
  "additional_properties": {}
}
```

`resume` обязателен; ответ возвращает обновлённую wizard schema с условиями и
ошибками полей. Клиент не должен прибивать обязательность полей гвоздями: она
зависит от schema/conditions, локали и состояния профиля.

Реализованный declarative transport принимает native-секции в state по путям
`/resumes/<id>/{profile,resume,creds,additional_properties}/...`. Он читает
свежий `resume_profile`, рекурсивно накладывает только объявленные object fields,
атомарно заменяет объявленные массивы, отправляет полный update body и проверяет
результат повторным GET. Командный envelope и пример описаны в
[`profile-bootstrap.md`](profile-bootstrap.md).

Для browser-only профиля проверен отдельный web-контракт: чтение
`GET /applicant/resume?resume=<id>` возвращает объект `resume`, а частичный
`POST /applicant/resume/edit?resume=<id>` принимает поля редактора. В desired
state они находятся под `/resumes/<id>/web/...`; writer использует allowlist,
не отправляет status/telemetry из GET и подтверждает результат повторным чтением.

## Profile fields

Отдельная часть `profile` включает:

- ФИО, дату рождения и пол;
- город, координаты и метро;
- гражданство и разрешения на работу;
- готовность к переезду и предпочтительные районы;
- образование;
- языки;
- водительские права и наличие автомобиля;
- способы связи и другие communication methods.

Контакты и личные данные считаются secret/sensitive. Diff и audit хранят имена
полей и факт изменения; plaintext старого/нового значения сохраняется только
там, где это действительно нужно для rollback/versioning, в зашифрованном виде.

## Resume fields

Основные группы `resume`:

- `title`, `professional_roles`, locale и platform;
- `salary` (amount/currency);
- `employment_form`/`employments`, `work_format`, `schedules`;
- `travel_time`, `business_trip_readiness`, relocation;
- `experience[]`: компания, позиция, даты, описание, область и отрасли;
- `skill_set` и свободный `skills`/about;
- образование, сертификаты и аттестации;
- языки;
- portfolio, sites и рекомендации;
- visibility/access, hidden fields и контакты.

Массивы в legacy edit API полностью заменяют прежнее значение. Resume-profile
client тоже должен строить update из свежей schema, а не отправлять partial
array вслепую.

## Browser observation

В меню карточки резюме редактирование открывается через
`resume-list-action-more` / `operations-list-edit-resume`. Наблюдаемые partial
edit routes и selectors:

### Position

Route: `/resume/edit/<id>/position`.

- `resume-edit-title-suggest`;
- `resume-salary-amount` и currency radios;
- professional role;
- `resume-edit-employment-forms`;
- `resume-edit-work-formats`;
- `resume-edit-travel-time`;
- `resume-edit-business-trip-readiness`;
- `resume-partial-edit-save` / `resume-partial-edit-cancel`.

### About и секции

- `/resume/edit/<id>/about`;
- `resume-editor-about`;
- `resume-edit-button-contacts`;
- `skill-level-open-editor`;
- `skills-add`;
- `resume-edit-button-education-0`;
- `resume-edit-button-about`;
- `skills-methods_go-to-tests-link`.

Эти selectors нужны для smoke tests и browser fallback. Основной editor UI
строится по API schema и отправляет API update.

## Versioning и безопасное редактирование

Изменение резюме — внешнее действие, влияющее на профиль и видимость. Workflow:

```text
read current schema
-> build proposed patch
-> validate dictionaries/conditions
-> show semantic diff
-> approval policy
-> update
-> read back and verify
-> optional publish when allowed
```

LLM может предложить перефразирование, но update получает только итоговый patch,
а не полный бесконтрольный rewrite. Для experience/about сохраняются версии и
semantic diff. Создание нового резюме сначала проверяет availability и при
клонировании явно фиксирует source resume.
