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
    "resume": "0123456789abcdef",
    "resume_aliases": {"backend": "fedcba9876543210"},
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
| `resume` | string | нет | — | ID резюме на платформе или alias из `resume_aliases`. Нужен для откликов, поднятия резюме и tailoring. |
| `resume_aliases` | object | нет | — | Карта `alias → platform resume ID`. Позволяет обращаться к резюме по короткому имени и использовать разные резюме в jobs/searches. |
| `resume_facts_file` | string | нет | — | Файл с явными фактами и плейсхолдерами (см. ниже). Обязателен, только если включена model policy письма или tailoring about. |
| `credentials_ref` | string | нет | — | Ссылка на OAuth-запись. Альтернатива `state_file`; если оба заданы, API-first, browser — как fallback. |
| `state_file` | string | нет | — | Файл browser storage state профиля (Playwright JSON). Даёт browser-транспорт и нужен для session refresh. |
| `contacts` | object | нет | — | Fallback для имени и контактов; рабочая площадка отдаёт их сама при старте (см. ниже). |
| `enabled` | bool | **да** | — | Участвует ли профиль в работе. Выключенный профиль не биндит сессии и не запускает свои jobs/searches. |
| `bootstrap` | object | нет | — | Первичное декларативное заполнение профиля (см. [Bootstrap](../../profile-bootstrap.md)); сейчас поддерживается только `when: empty`. |
| `applications` | object | нет | — | Политика откликов: [applications](applications.md). |
| `conversations` | object | нет | — | Политика чатов: [conversations](conversations.md). |
| `answers` | object | нет | — | Резолвер неизвестных вопросов: `answers.model` (см. ниже). |

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

## Связанные документы

- [Отклики и письма](applications.md)
- [Tailoring](tailoring.md)
- [Чаты](conversations.md)
- [Bootstrap](../../profile-bootstrap.md)
- [Desired state](../../profile-desired-state-and-routing.md)
