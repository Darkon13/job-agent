# Job Agent

Локальный сервис для автоматизации работы с агрегаторами вакансий,
переписками с рекрутерами и календарём.

Проект строится вокруг декларативной конфигурации, адаптеров платформ,
операторных цепочек и событийной обработки. Подход к конфигурации вдохновлён
Xray и sing-box. Идея проекта появилась как самостоятельное развитие
архитектурных идей
[hh-applicant-tool](https://github.com/s3rgeym/hh-applicant-tool).

Реализация пишется заново. Код сторонних проектов может использоваться только
с соблюдением их лицензий.

## Архитектура

```text
config -> router -> workflows -> adapter facade
                                  |- platform API
                                  `- browser worker

events -> broker -> deterministic / LLM / MCP / human operators
```

- `core` — общие доменные типы и capabilities.
- `adapter` — контракт, registry и создание адаптеров.
- `adapters/*` — реализации для конкретных платформ.
- `config` — загрузка и проверка декларативной конфигурации.
- `cmd/job-agent` — точка запуска сервиса.

Поисковые фильтры принадлежат адаптеру. Общими остаются маршрутизация,
приоритеты, цели по количеству откликов и fallback-цепочки.

## Текущее состояние

Core уже содержит:

- типизированные идентификаторы платформ, профилей, вакансий, откликов и задач;
- capability-модель и нормализованные категории ошибок адаптеров;
- жизненный цикл вакансии, профиля, отклика и фоновой задачи;
- неизменяемый envelope доменных событий с correlation/causation ID;
- переносимые блоки ответов, не зависящие от порядка вопросов и runtime ID;
- канонический каталог тестов, уровни квалификаций и revision-safe human review
  для REST/TG/CLI;
- нормализованные диалоги и сообщения, а также отменяемые одноразовые follow-up
  таймеры с idempotency, deadline и проверкой входящего ответа перед отправкой;
- одноразовый idempotent bootstrap чистого профиля из mounted JSON.

Первый search workflow вызывает платформенный поиск один раз и распределяет
нормализованные вакансии между целевыми профилями. In-memory repository хранит
одну вакансию на `(platform, external_id)` и один отклик на
`(profile, vacancy)`. In-memory broker дедуплицирует задачи по стабильному
idempotency key; повторный запуск восстанавливает постановку задачи, если
предыдущая попытка успела сохранить отклик, но не дошла до очереди.

Для постоянного состояния доступен SQLite store: он реализует те же
repository/broker-порты и сохраняет vacancies, discoveries, applications и
idempotent tasks в одном файле. In-memory реализации остаются для быстрых
unit-тестов. Путь задаётся через `database.path`.

Миграции не выполняются при старте основного сервиса. Отдельный entrypoint на
`golang-migrate` применяет embedded versioned SQL; после этого `job-agent`
проверяет точную schema version и отказывается работать с отсутствующей,
устаревшей или dirty-схемой:

```sh
go run ./cmd/job-agent-migrate -config ./config/example/config.json up
go run ./cmd/job-agent ./config/example/config.json
```

Доступны `version`, пошаговый `up -steps N`, bounded rollback
`down -steps N` и аварийный `force VERSION`. Неограниченный `down` запрещён.
Базы раннего прототипа с `PRAGMA user_version` 1–3 проверяются по ожидаемым
таблицам/колонкам и один раз переводятся на `schema_migrations`; неизвестная или
несовместимая legacy-схема автоматически не принимается.

SQLite и memory stores также реализуют progressive test catalog и human review
history. Каталог создаётся до начала попытки и пополняется вопросами независимо;
изменённый набор вариантов сохраняется под новым question fingerprint. Review
prompt включает полный runtime-вопрос, а выбор пользователя append-ится вместе
с атомарным CAS по session revision, поэтому повторные REST/TG callbacks не
могут отправить два ответа.

Conversation storage сохраняет нормализованные диалоги, упорядоченный timeline
сообщений и follow-up таймеры. Внешние chat/message ID и idempotency key
дедуплицируются, обновление таймера защищено revision CAS, а выборка scheduled
таймеров по `run_at` позволяет восстановить отложенные broker tasks после
рестарта.

Consumer очереди атомарно получает задачу вместе с ограниченным lease и
случайным token. Поддерживаются heartbeat/extend, complete, delayed retry и
terminal fail. После падения worker задача повторно выдаётся по окончании lease,
а запоздалый результат старого worker отклоняется.

Fallback между transport-реализациями разрешён только для операции, которую
текущий transport не поддерживает. Ошибки авторизации, валидации, rate limit и
временные сбои не маскируются переключением на другой transport.

HH-адаптер умеет проверять собственную поисковую конфигурацию. Сетевые и
браузерные операции ещё не реализованы.

```sh
go test ./...
go run ./cmd/job-agent-migrate -config ./config/example/config.json up
go run ./cmd/job-agent ./config/example/config.json
```

Исследованные контракты первого HH-адаптера находятся в `docs/`: public API,
авторизация, global/similar search, chatik, applicant browser operations и
resume/profile schema. REST и automation-контракт чатов описан в
`docs/conversation-api.md`. Главная архитектурная спецификация — `AGENTS.md`.

## Questionnaire mock

Локальный mock показывает single-choice вопросы, перемешивает порядок и runtime
ID и экспортирует выбранные ответы как переносимый qualification `AnswerBlock`
с fingerprint каждого отдельного вопроса:

```sh
go run ./cmd/questionnaire-mock
```

После запуска открыть `http://127.0.0.1:8090`. Один JSON содержит один небольшой
блок с `tag`, `name`, `kind` и `platform`. Готовый файл можно загрузить через
file input; ответы восстанавливаются по `question_fingerprint` и тексту
варианта, а не по позиции, runtime ID или заранее известному составу всего
теста. Пример draft-блока находится в
`config/example/mock-answer-block.json`.

Целевой интерактивный flow умеет запускать тест явной командой, отправлять
каждый вопрос с полным списком вариантов в localhost UI, REST/SSE, Telegram или
CLI и продолжать попытку после выбора пользователя. Все варианты остаются в
`TestDefinition`, а выборы записываются отдельными ревизиями. Задания с кодом
пока только каталогизируются и не отправляются автоматически.
