# `profiles[].applications`

Политика откликов профиля: режим, источник письма, фильтры, лимиты и
подстройка резюме.

## Пример

```json
"applications": {
  "mode": "submit",
  "message_template_file": "messages/backend.json",
  "qualification": {
    "title_include_any": ["Golang", "DevOps", "backend", "поддержк"],
    "include_any": ["Go", "Golang", "Backend"],
    "exclude_any": ["руководитель", "team lead", "дизайн", "менеджер"]
  },
  "daily_limit": 200,
  "submit_jitter": {"min": "1s", "max": "3s"},
  "timezone": "Europe/Moscow",
  "allow_visibility_change": false,
  "tailoring": {"about": {"enabled": true}}
}
```

| Поле | Тип | Обяз. | По умолчанию | Описание |
|---|---|---|---|---|
| `mode` | string | нет | `dry_run` | `dry_run` — готовит и логирует, не отправляет; `approval` — ждёт подтверждения; `submit` — отправляет. Tailoring требует `submit`. |
| `message` | string | нет | — | Статический текст письма без подстановок. |
| `message_template` | string | нет | — | Inline Go-шаблон письма. |
| `message_template_file` | string | нет | — | Файл пула писем: формат и стратегии — [Письма и пулы](../messages.md). |
| `model` | object | нет | — | Генерация письма моделью; требует ровно одного fallback-источника письма. |
| `employer_rules` | array | нет | — | Правила по работодателям (см. ниже). Первое совпадение выигрывает. |
| `qualification` | object | нет | — | Текстовые фильтры вакансии (см. ниже). |
| `daily_limit` | number | нет | дефолт платформы (HH 200) | Дневной потолок успешных откликов на профиль/платформу. |
| `submit_jitter` | object | нет | HH: `1s`..`3s` | Пауза между отправками: `min`, `max` (Go duration). Разброс применяется до claim, worker не держит lease. |
| `timezone` | string | нет | — | IANA-таймзона для расписаний и дневных лимитов, например `Europe/Moscow`. |
| `allow_visibility_change` | bool | нет | `false` | Разрешить сервису менять видимость резюме на платформе при отклике. |
| `validation_action` | string | нет | `review` | Что делать с вакансией, требующей анкету или тест: `review` — ждать ответа оператора (`waiting_validation`), `skip` — пометить отклик `skipped`, освободить очередь и дать «Повторить» после заполнения анкеты. |
| `tailoring` | object | нет | — | Временная подстройка резюме: [tailoring](tailoring.md). |

Ровно один из `message`, `message_template`, `message_template_file`. Если
задан `model`, fallback-источник обязателен, а профилю нужен
`resume_facts_file`.

`validation_action: skip` не создаёт review-сессию, не тратит бюджет и не
блокирует поток: отклик получает `skipped` с кодом `questionnaire_required` /
`vacancy_test_required` / `platform_validation_required`, а в dashboard у него
появляется кнопка «Повторить» — нажмите её после заполнения анкеты на
платформе, и отклик вернётся в `ready`.

## `applications.model`

| Поле | Тип | Обяз. | Описание |
|---|---|---|---|
| `provider` | string | **да** | `tag` из `models[]`. |
| `prompt_version` | string | **да** | Версия промпта (для provenance и A/B). |
| `instruction` | string | **да** | Пользовательская часть промпта. |
| `timeout` | string | **да** | Таймаут вызова, Go duration. |

!!! warning "Ещё не реализовано"
    Модель письма пока получает только `resume_facts_file` и контекст вакансии.
    Генерация из живого резюме («О себе» + описания работ) и выбор источников
    (`experience:1`, `custom:<tag>`) спроектированы, но ещё не реализованы.

Модель получает обезличенный структурированный контекст (вакансия, факты
резюме, контакты-маски) и возвращает письмо с evidence-подтверждениями.
Локальный validator отклоняет: незаполненные плейсхолдеры, служебный
JSON/code-fence, числа/URL/e-mail, которых нет во входном контексте, и
evidence, не покрывающие текст. Ошибка модели не отправляет письмо — operator
выбирает fallback.

В `instruction` нельзя хардкодить реальные контакты (`@user`, `user@example`):
модель получит их как готовый текст, а validator увидит e-mail, которого нет в
обезличенном контексте, и отправит письмо в fallback. Используйте ровно
`{telegram}` и `{email}` — сервис подставит значения после проверки.

## `applications.employer_rules`

```json
"employer_rules": [
  {"employer_groups": ["marketplaces"], "action": "skip"},
  {"employer_groups": ["known-good"], "action": "message_pool", "message_template_file": "messages/good.json"},
  {"employer_groups": ["banking"], "action": "review"}
]
```

| Поле | Тип | Обяз. | Описание |
|---|---|---|---|
| `employer_groups` | array | **да** | Одна или несколько групп из `employer_groups[]`. |
| `action` | string | **да** | `skip` — не откликаться; `review` — в ручную проверку; `message_pool` — свой пул писем; `model` — своя model policy. |
| `message_template_file` | string | нет | Пул писем для `message_pool`/`model`. |
| `model` | object | нет | Model policy правила; требует пул как fallback. |

Правила применяются по порядку, первое совпадение останавливает перебор.
Причина решения (`группа`, тип совпадения) сохраняется в отклике.

## `applications.qualification`

`title_include_any` смотрит только на название вакансии. Это главный фильтр
для широких поисков: HH часто находит вакансию потому, что нужное слово есть
в описании, а сама роль другая (например, «Графический дизайнер» в выдаче по
«1С»). `include_any`/`include_all` читают заголовок, работодателя, описание и
key skills; `exclude_any` и `exclude_all` читают заголовок и работодателя
(описание не участвует, чтобы упоминание «1С» в тексте не исключало
подходящую роль). Если заданы сразу несколько include-фильтров, должны
выполниться все.

| Поле | Тип | Обяз. | Описание |
|---|---|---|---|
| `title_include_any` | array | нет | Вакансия проходит, если в её названии есть хотя бы один термин. Рекомендуется для live-откликов. |
| `include_any` | array | нет | Вакансия проходит, если полный текст содержит хотя бы один термин (OR). Пусто — без фильтра. |
| `include_all` | array | нет | Вакансия проходит, только если полный текст содержит все термины (AND). Пусто — без фильтра. |
| `exclude_any` | array | нет | Любой термин в названии или работодателе переводит вакансию в `skipped` (`excluded_term`). |
| `exclude_all` | array | нет | Вакансия исключается, только если в полном тексте присутствуют все термины сразу. |

Матчинг без учёта регистра и по границам слов: `Go` не совпадает с `Django`.
Это клиентский слой поверх любого источника поиска — включая
`related_vacancy`, где платформенные фильтры недоступны.

!!! warning "Ограничение"
    Привязка `include_any`/`include_all` к отдельным полям (`description`,
    `key_skills`) пока не реализована: они читают объединённый текст. Для
    точного отсева неподходящих ролей используйте `title_include_any` и
    `exclude_any`.
