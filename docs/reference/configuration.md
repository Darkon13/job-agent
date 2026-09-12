# Справочник конфигурации

Полное описание ключей декларативной конфигурации. Формат вдохновлён Xray и
sing-box: объекты имеют `tag`, ссылаются друг на друга по имени и
объединяются в include-файлы.

Начните с [`../quickstart.md`](../quickstart.md), а сюда возвращайтесь за
точными именами полей и допустимыми значениями.

## Как читается конфигурация

- `schema_version` обязателен при ломающих изменениях; текущая версия — `1`.
  Неизвестная версия отклоняется.
- `include` содержит пути или glob-маски относительно своего файла.
  Include-файлы объединяются depth-first после основного файла и добавляют
  только коллекции (`adapters`, `profiles`, `searches`, `jobs`, `models`,
  `employer_groups`, `resources`). `database` и `server` допустимы только в
  главном файле.
- Повтор `tag` между файлами запрещён; glob без совпадений — ошибка.
- Ссылки на файлы внутри любого include (`message_template_file`,
  `resume_facts_file`, `bootstrap.source`) считаются от каталога файла, который
  их объявил.
- Секреты в конфиг не пишутся: только `credentials_ref`, `state_file` или
  переменные окружения.

## Верхний уровень

| Ключ | Тип | Описание |
|---|---|---|
| `schema_version` | number | версия схемы конфига, сейчас `1` |
| `database` | object | SQLite-хранилище |
| `server` | object | HTTP API и scheduler |
| `adapters` | array | экземпляры адаптеров платформ |
| `models` | array | провайдеры моделей для operator-цепочек |
| `profiles` | array | аккаунты платформ |
| `searches` | array | поисковые объекты |
| `jobs` | array | расписания и actions |
| `resources` | array | desired state профиля (`profile_state`) |
| `employer_groups` | array | именованные группы работодателей для правил |
| `answer_sets` | array | пути к файлам ответов (`AnswerBlock`) |
| `include` | array | подключаемые файлы и маски |

### `database`

| Ключ | Тип | Описание |
|---|---|---|
| `driver` | string | только `sqlite` |
| `path` | string | путь к файлу БД; каталог создаётся автоматически |

### `server`

| Ключ | Тип | Описание |
|---|---|---|
| `listen` | string | адрес listener, по умолчанию `127.0.0.1:8080` |
| `api_token_env` | string | имя переменной с Bearer-токеном; без него разрешён только loopback |
| `exposure` | string | `loopback` или `private` (для непубличной сети контейнера) |
| `follow_up_reconcile_interval` | duration | период сверки follow-up таймеров |
| `scheduler_reconcile_interval` | duration | период сверки cron-расписаний |

Переменные окружения `JOB_AGENT_SERVER_LISTEN` и
`JOB_AGENT_SERVER_EXPOSURE` переопределяют listener для контейнера.

### `adapters`

| Ключ | Тип | Описание |
|---|---|---|
| `tag` | string | имя экземпляра адаптера |
| `type` | string | тип из registry, сейчас `hh` |
| `settings` | object | настройки адаптера |

Настройки HH (`type: hh`):

| Ключ | Тип | Описание |
|---|---|---|
| `api_delay_ms` | number | пауза между API-запросами, мс |
| `user_agent` | string | User-Agent для API-запросов |

### `models`

| Ключ | Тип | Описание |
|---|---|---|
| `tag` | string | имя провайдера, на него ссылаются policy |
| `type` | string | тип провайдера (OpenAI-совместимый) |
| `model` | string | имя модели |
| `base_url` | string | необязательный свой endpoint |
| `api_key_env` | string | переменная с ключом, по умолчанию `OPENAI_API_KEY` |
| `max_output_tokens` | number | предел ответа модели |

## `profiles`

| Ключ | Тип | Описание |
|---|---|---|
| `tag` | string | **произвольное имя профиля**, используется в ссылках |
| `adapter` | string | ссылка на `adapters[].tag` |
| `resume` | string | ID резюме на платформе или alias из `resume_aliases` |
| `resume_aliases` | object | карта `alias → platform resume ID` |
| `resume_facts_file` | string | файл с фактами резюме для model/template контекста |
| `credentials_ref` | string | ссылка на OAuth-креденшелы (альтернатива cookies) |
| `state_file` | string | файл browser storage state профиля |
| `enabled` | bool | участвует ли профиль в работе |
| `bootstrap` | object | первичное декларативное заполнение (см. `profile-bootstrap.md`) |
| `applications` | object | политика откликов |
| `conversations` | object | политика чатов |
| `answers` | object | резолвер неизвестных вопросов |

Пример алиасов:

```json
"resume_aliases": {"backend": "resume-id-1", "golang": "resume-id-2"}
```

### `profiles[].applications`

| Ключ | Тип | Описание |
|---|---|---|
| `mode` | string | `dry_run` (ничего не отправляет), `approval` (ждёт approve), `submit` (отправляет) |
| `message` | string | статический текст сопроводительного |
| `message_template` | string | inline-шаблон |
| `message_template_file` | string | файл пула шаблонов (`strategy: first\|stable_hash`) |
| `model` | object | `provider`/`prompt_version`/`instruction`/`timeout` для генерации письма |
| `employer_rules` | array | правила по работодателям: `employer_groups`, `action` (`skip`, `review`, `message_pool`), свой пул или model |
| `qualification` | object | `include_any`, `exclude_any` — текстовые фильтры по вакансии |
| `daily_limit` | number | дневной потолок откликов на профиль/платформу |
| `submit_jitter` | object | пауза между отправками: `min`, `max` (например, `15s`/`30s`) |
| `timezone` | string | таймзона рассылок и расписаний |
| `allow_visibility_change` | bool | разрешить менять видимость резюме при отклике |
| `tailoring` | object | временная подстройка резюме перед откликом |

Ровно один из `message`, `message_template`, `message_template_file` — если
указан `model`, fallback-источник обязателен.

### `profiles[].conversations`

| Ключ | Тип | Описание |
|---|---|---|
| `allow_send` | bool | разрешить отправку сообщений (по умолчанию выключено) |
| `allow_mark_read` | bool | разрешить менять read-state чатов |
| `answer_known` | bool | отвечать на опросники просмотренными блоками из `answer_sets` (требует `allow_send`) |

### `profiles[].answers`

```json
"answers": {
  "model": {
    "provider": "openai",
    "prompt_version": "v1",
    "instruction": "Отвечай только вариантом из списка",
    "timeout": "30s"
  }
}
```

## `searches`

| Ключ | Тип | Описание |
|---|---|---|
| `tag` | string | имя поиска, используется в `fallback` и `routes` |
| `adapter` | string | ссылка на адаптер |
| `profiles` | array | для каких профилей выполняется поиск |
| `priority` | number | порядок обхода маршрутов |
| `target_applications` | number | сколько вакансий собрать из этого поиска за проход |
| `fallback` | string | следующий `search.tag`, если выдача исчерпана |
| `query` | object | adapter-specific фильтры |

Ключи `query` для HH, `source` обязателен:

| Ключ | Значения |
|---|---|
| `source` | `global`, `similar_resume`, `similar_vacancy`, `related_vacancy` |
| `resume` | ID резюме; обязателен для `similar_resume`, запрещён для `global` |
| `vacancy` | ID вакансии; обязателен для vacancy-based источников |
| `text` | строка поиска |
| `area` | ID регионов: `1` — Москва, `2` — Санкт-Петербург (`https://api.hh.ru/areas`) |
| `professional_role` | ID профессиональных ролей |
| `experience` | `noExperience`, `between1And3`, `between3And6`, `moreThan6` |
| `employment` | `full`, `part`, `project`, `probation`, `volunteer` |
| `schedule` | `remote`, `fullDay`, `flexible`, `shift`, `flyInFlyOut` |
| `industry`, `employer_id`, `excluded_employer_id` | отрасли и работодатели |
| `salary`, `currency`, `only_with_salary` | зарплатный фильтр |
| `period`, `date_from`, `date_to` | окно публикации |
| `order_by` | `publication_time`, `salary_desc`, `relevance` |
| `page_size`, `max_pages` | пагинация за один проход |
| `label`, `search_field`, `metro` | дополнительные фильтры |

## `jobs`

| Ключ | Тип | Описание |
|---|---|---|
| `tag` | string | имя job, используется в API и dashboard |
| `enabled` | bool | включено ли расписание |
| `priority` | number | приоритет задачи в очереди |
| `concurrency` | string | `forbid`, `forbid_per_profile` или `allow` |
| `triggers` | array | cron, event или оба |
| `action` | object | что выполнять |

### Триггеры

```json
{"type": "cron", "expression": "30 9 * * *", "timezone": "Europe/Moscow", "misfire": "run_once",
 "jitter": {"min": "1m", "max": "10m"}}
```

| Ключ | Описание |
|---|---|
| `type` | `cron` или `event` |
| `expression` | cron-выражение (5 полей) |
| `timezone` | явная таймзона, по умолчанию берётся из профиля |
| `misfire` | `run_once` — один догоняющий запуск |
| `jitter` | случайная задержка старта `min`/`max` |
| `event` / `filters` | для `type: event` — имя события и фильтры по атрибутам |

### Actions

| `type` | Поля | Что делает |
|---|---|---|
| `application.campaign` | `profiles`, `routes`, `target_successful`, `max_in_flight` | запускает отклики по маршрутам до цели |
| `application.retention` | `profile`, `retention: {stale_after, remove_rejected}` | локальная очистка старых откликов |
| `resume.touch` | `profile` | поднимает резюме |
| `resume.publish` | `profile`, `resume` | публикует резюме |
| `resume.update` | `profile`, `resource`, `publish` | plan → apply → read-back для desired state |
| `profile.activity.observe` | `profile` | снимок активности резюме |
| `profile_state.reconcile` | `resource` | сверяет объявленный resource с платформой |
| `conversation.sync` | `profile` | синхронизирует историю чатов |
| `conversation.follow_up.select` | `profile`, `follow_up` | выбирает чат и планирует напоминание |

## `resources`

Декларативный desired state профиля (резюме, личные поля) с безопасным
plan/apply. Подробности — в
[`../profile-desired-state-and-routing.md`](../profile-desired-state-and-routing.md).

| Ключ | Описание |
|---|---|
| `tag` | имя resource |
| `type` | `profile_state` |
| `profile` | ссылка на профиль |
| `ownership` | `declared_fields` — владеет только объявленными листьями |
| `state` | объявленные поля: `resumes.<id>/{web,web_profile,...}` для browser или `resumes.<id>/{profile,resume,creds,additional_properties}` для API |

## `answer_sets`

Пути к файлам `AnswerBlock` (`qualification`, `conversation`, `vacancy`).
Один файл — один небольшой блок с `tag`, `name`, `kind`, `platform` и
ответами. Блоки с одним и тем же `tag` запрещены. Контракт ответов — в
[`../next-answer-model-fallback.md`](../next-answer-model-fallback.md).

## Счётчики и лимиты

| Поле | Где | Смысл |
|---|---|---|
| `target_applications` | `searches[]` | сколько вакансий собрать из поиска |
| `target_successful` | `application.campaign` | цель по подтверждённым откликам |
| `max_in_flight` | `application.campaign` | параллельные отклики |
| `daily_limit` | `profiles[].applications` | дневной потолок на профиль/платформу |
| `submit_jitter` | `profiles[].applications` | пауза между отправками |

## Переменные окружения

| Переменная | Назначение |
|---|---|
| `JOB_AGENT_SERVER_LISTEN` | переопределяет `server.listen` (контейнер) |
| `JOB_AGENT_SERVER_EXPOSURE` | переопределяет `server.exposure` |
| `JOB_AGENT_API_TOKEN` | Bearer-токен, имя задаётся в `server.api_token_env` |
| `BROWSER_WORKER_URL` / `BROWSER_WORKER_TOKEN` | адрес и токен browser worker |
| `OPENAI_API_KEY` | ключ модели по умолчанию для `models` |
