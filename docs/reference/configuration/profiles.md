# `profiles`

Профиль — это учётная запись на платформе, а не отдельный процесс. Один инстанс
обслуживает несколько профилей; состояние профиля изолировано (credentials,
browser context, data).

## Пример

```json
"profiles": [
  {
    "tag": "primary",
    "adapter": "hh-main",
    "resumes": [
      {"id": "0123456789abcdef", "title": "Go developer", "primary": true},
      {"id": "fedcba9876543210", "title": "Backend"}
    ],
    "resume_aliases": {"go": "0123456789abcdef", "backend": "fedcba9876543210"},
    "identity": {"display_name": "Антон", "email": "a***@example.test", "captured_at": "2026-09-27T11:20:31Z"},
    "resume_facts_file": "facts/primary.json",
    "state_file": "./data/profiles/primary.json",
    "contacts": {"email": "user@example.test", "telegram": "@user"},
    "enabled": true,
    "applications": {"mode": "submit"},
    "conversations": {"allow_send": true, "answer_known": true}
  }
]
```

| Поле | Тип | Обяз. | По умолчанию | Описание |
|---|---|---|---|---|
| `tag` | string | **да** | — | Имя профиля. Используется в ссылках (`profiles`, `target_profiles`) и как ключ состояния. |
| `adapter` | string | **да** | — | `tag` из `adapters[]`. |
| `resumes` | array | нет | — | Список резюме аккаунта: `{"id": "...", "title": "...", "primary": true}`. Профиль — это учётная запись, а не резюме: список может быть пустым, ровно одно резюме основное. Первое объявление `primary` побеждает, иначе основным становится первый элемент. |
| `identity` | object | нет | — | Кэш безопасных данных аккаунта: `display_name`, маскированные `email`/`phone`, `account_hash`, `captured_at`. Дашборд показывает его до входа, поэтому не храните здесь токены и полные персональные данные. |
| `resume` | string | нет | — | Устаревшее основное резюме. Работает как раньше, но нормализуется в `resumes[]`; при непустом `resumes` должен быть в списке. |
| `resume_aliases` | object | нет | — | Карта `alias → platform resume ID`: короткое имя для ссылок в jobs/searches. При непустом `resumes` каждый alias должен указывать на резюме из списка. |
| `resume_facts_file` | string | нет | — | Файл с явными фактами и плейсхолдерами (см. ниже). Обязателен, только если включена model policy письма или tailoring about. |
| `credentials_ref` | string | нет | — | Ссылка на OAuth-запись. Альтернатива `state_file`; если оба заданы, API-first, browser — как fallback. |
| `state_file` | string | нет | — | Файл browser storage state профиля (Playwright JSON). Даёт browser-транспорт и нужен для session refresh. |
| `contacts` | object | нет | — | Fallback для имени и контактов; рабочая площадка отдаёт их сама при старте (см. ниже). |
| `enabled` | bool | **да** | — | Участвует ли профиль в работе. Выключенный профиль не биндит сессии и не запускает свои jobs/searches. |
| `bootstrap` | object | нет | — | Первичное декларативное заполнение профиля (см. [Bootstrap](../../profile-bootstrap.md)); сейчас поддерживается только `when: empty`. |
| `applications` | object | нет | — | Политика откликов: [applications](applications.md). |
| `conversations` | object | нет | — | Политика чатов: [conversations](conversations.md). |
| `answers` | object | нет | — | Резолвер неизвестных вопросов: `answers.model` (см. ниже). |

## `profile_store`: профильные фрагменты

Дашборд и CLI добавляют профили через логин и пишут каждый профиль отдельным
файлом-фрагментом, а не правят основной конфиг. Каталог задаётся в главном
файле один раз:

```json
"profile_store": {"dir": "./data/profile-store"}
```

| Поле | Тип | Обяз. | По умолчанию | Описание |
|---|---|---|---|---|
| `dir` | string | нет | `<каталог database.path>/profile-store` | Каталог фрагментов. Относительный `dir` резолвится от файла конфига; относительный `database.path` сохраняет базис процесса, с которым открывается база. |

Правила:

- каждый top-level `*.json` в каталоге загружается как обычный include-фрагмент
  (`profiles`, а в будущем `jobs`) и виден в `GET /api/v1/profiles` с
  `"source": "profile_store"`;
- ссылки на файлы внутри фрагмента резолвятся относительно самого фрагмента;
- отсутствующий или пустой каталог — не ошибка, поэтому конфиг работает и до
  первого профиля;
- `profile_store` допустим только в главном файле; повтор `tag` с профилем из
  основного конфига отклоняется;
- сессия профиля лежит в `<dir>/<tag>/state.json`, если `state_file` не задан
  явно.

## `profiles[].contacts`

| Поле | Тип | Обяз. | Описание |
|---|---|---|---|
| `first_name` | string | нет | Имя отправителя. |
| `last_name` | string | нет | Фамилия. |
| `email` | string | нет | Email; должен содержать `@`. |
| `telegram` | string | нет | Telegram (обычно `@handle`). |

!!! note "Тестируется"
    Авторазрешение контактов проверено живьём на browser-профилях. Если
    платформенное чтение недоступно (мёртвая сессия), сервис логирует
    предупреждение и использует значения из `contacts`.

При старте сервис читает `firstName`, `lastName`, `web/email` и
`communicationMethods` из live-профиля; значения с площадки приоритетнее, а
`contacts` заполняет пропущенное. В шаблонах письма доступны
`{{.Profile.*}}`, для модели заменяются масками `{first_name}`, `{last_name}`,
`{email}`, `{telegram}` и подставляются обратно после валидации. Контакты —
персональные данные: не коммитьте их и держите файл конфига локально.

## `profiles[].resume_facts_file`

```json
{
  "resume_id": "0123456789abcdef",
  "facts": {
    "position": "Go developer",
    "skills": ["Go", "PostgreSQL"],
    "placeholders": {"name": "Иван", "surname": "Иванов"}
  }
}
```

- `resume_id` (**да**) должен совпадать с резюме профиля: контекст tailoring
  строится на этом резюме; несовпадение отклоняется при загрузке.
- `facts` (**да**) — вложенный объект с явными фактами: только они и
  наблюдаемое резюме попадают в model-контекст.
- `placeholders` — зарезервированный ключ: значения заменяются масками перед
  вызовом модели и подставляются после валидации. `company_name` объявлять
  нельзя — работодатель маскируется автоматически.

## `profiles[].answers`

| Поле | Тип | Обяз. | Описание |
|---|---|---|---|
| `model` | object | нет | Провайдер для ответов на неизвестные вопросы квалификационных тестов. |

`answers.model` повторяет формат model policy:

| Поле | Тип | Обяз. | Описание |
|---|---|---|---|
| `provider` | string | **да** | `tag` из `models[]`. |
| `prompt_version` | string | **да** | Версия промпта для provenance. |
| `instruction` | string | **да** | Инструкция модели. |
| `timeout` | string | **да** | Таймаут вызова, Go duration (`30s`). |

## Системные джобы профиля

Фоновая жизнь аккаунта описана политиками, а не джобами: сервис сам создаёт
`system.*` расписания и не даёт редактировать их как обычные jobs. Каждая
политика принимает `enabled`, `interval` (Go duration) и необязательный
`jitter`; выключение политики убирает её расписания.

```json
"state_harvest": {"enabled": true, "interval": "30m"},
"resume_touch": {"enabled": true},
"activity_maintain": {"enabled": true, "query": {"source": "global", "text": "Go"}},
"application_cleanup": {
  "enabled": true,
  "interval": "3h",
  "retention": {"stale_after": "720h", "remove_rejected": true, "remove_waiting_validation": true, "validation_stale_after": "168h"}
}
```

| Политика | По умолчанию | Джобы | Что делает |
|---|---|---|---|
| `state_harvest` | включена, `1h` | `system.state.chats`, `system.state.poll` (2m), `system.state.applications`, `system.state.activity` | Синхронизирует каталог и историю чатов, состояния откликов и снимки метрик резюме. `system.state.activity` создаётся, только если у профиля есть резюме. |
| `resume_touch` | включена, `4h` | `system.resume-touch` | Поднимает основное резюме; платформа сама сообщает `nextTouchAt`, задача ждёт разрешённого времени. |
| `activity_maintain` | включена, `1h` | `system.activity-maintain` | Открывает реальные вакансии-кандидаты браузерной сессией. `query` заменяет источник (по умолчанию общий фид без фильтров). |
| `application_cleanup` | **выключена**, `3h` | `system.cleanup` | Локально убирает устаревшие и отказанные отклики по `retention` (см. [Очистка откликов](../../application-cleanup.md)). |

Всегда включены `system.session` (обновление сессии HH раз в 4 часа) и
`system.validation` (перепроверка вакансий с анкетами и тестами раз в сутки) —
они зависят от возможностей профиля, а не от политики. Разброс запуска
системной джобы по умолчанию не превышает десятой части интервала и пяти минут;
явный `jitter` в политике переопределяет его. Приостановить системную джобу
можно как любую другую: `job-agent jobs pause <tag>` или кнопкой в дашборде.

## Связанные документы

- [Отклики и письма](applications.md)
- [Tailoring](tailoring.md)
- [Чаты](conversations.md)
- [Bootstrap](../../profile-bootstrap.md)
- [Desired state](../../profile-desired-state-and-routing.md)
