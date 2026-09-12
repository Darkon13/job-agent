# Конфигурация

Job Agent читает один JSON-файл. Файл может быть собран из нескольких через
`include`, но итоговая конфигурация существует только в памяти: приложение не
пишет её обратно.

Документация разложена по объектам — как в справочнике Xray, у каждого поля
указаны тип, обязательность и значение по умолчанию.

## Объекты

| Объект | Ключ | Что описывает |
|---|---|---|
| [database](configuration/database.md) | `database` | хранилище состояния (SQLite) |
| [server](configuration/server.md) | `server` | HTTP API, наблюдаемость, токен |
| [adapters](configuration/adapters.md) | `adapters` | инстансы платформ и их настройки |
| [models](configuration/models.md) | `models` | провайдеры моделей для писем и ответов |
| [profiles](configuration/profiles.md) | `profiles` | учётные записи, резюме, контакты |
| [applications](configuration/applications.md) | `profiles[].applications` | политика откликов и писем |
| [tailoring](configuration/tailoring.md) | `profiles[].applications.tailoring` | временная подстройка резюме |
| [conversations](configuration/conversations.md) | `profiles[].conversations` | чаты и автоответы |
| [searches](configuration/searches.md) | `searches` | поиски и фильтры платформы |
| [jobs](configuration/jobs.md) | `jobs` | расписание и действия |
| [resources](configuration/resources.md) | `resources` | декларативный desired state |
| [employer_groups](configuration/employer-groups.md) | `employer_groups` | группы работодателей |
| [answer_sets](configuration/answers.md) | `answer_sets` | файлы готовых ответов |
| [include](configuration/includes.md) | `include` | сборка конфига из файлов |
| [messages](messages.md) | `message_template_file` | формат пулов писем |

## Минимальный конфиг

```json
{
  "schema_version": 1,
  "database": {"driver": "sqlite", "path": "./data/job-agent.db"},
  "adapters": [{"tag": "hh-main", "type": "hh"}],
  "profiles": [{
    "tag": "primary",
    "adapter": "hh-main",
    "resume": "0123456789abcdef",
    "state_file": "./data/profiles/primary.json",
    "enabled": true,
    "applications": {"mode": "dry_run", "message": "Здравствуйте!"}
  }],
  "searches": [{
    "tag": "golang",
    "adapter": "hh-main",
    "profiles": ["primary"],
    "priority": 100,
    "target_applications": 10,
    "query": {"source": "global", "text": "Golang"}
  }]
}
```

## Общие правила

- **`tag` и ссылки.** Каждый объект с `tag` имеет уникальное имя в своём
  пространстве. Ссылки — строки с чужим `tag` (`adapter`, `profiles`,
  `routes`, `provider`, `operator`). Опечатка отклоняется при загрузке.
- **`schema_version`.** Необязательное поле, по умолчанию `1`. Другая версия
  отклоняется: ломающее изменение схемы требует новой версии и миграции.
- **Пути к файлам.** `resume_facts_file`, `message_template_file`,
  `bootstrap.source` и `answer_sets` разрешаются относительно файла, который
  их объявил, поэтому include-фрагменты переносимы.
- **Секреты.** Ключи моделей не хранятся в конфиге: указывается только имя
  переменной окружения (`api_key_env`). Credentials, cookies и browser state —
  в `state_file`/`credentials_ref`, права `0600`, вне Git.
- **Проверка.** Конфиг валидируется при старте: сервис не запустится с
  неразрешённой ссылкой, неизвестным `type`, отрицательным лимитом или
  синтаксически неверным шаблоном. Состояние работающего сервиса показывает
  `job-agent-check`.
- **Изменения на ходу.** Изменение определения поиска (`query`, профили,
  adapter) автоматически начинает новую generation с чистым курсором; смена
  `config.json` требует перезапуска сервиса.

См. также [рецепты](recipes.md) — готовые конфигурации для частых задач.
