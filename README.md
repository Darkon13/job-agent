# Job Agent

Локальный сервис для автоматизации поиска вакансий, подготовки и массового
распределения откликов между профилями. Переписки с рекрутерами и календарь —
дополнительные контуры, которые включаются после основного application flow.

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

Global HH search выполняется отдельными задачами по одной странице. У обычного
discovery-поиска durable `search_runs` владеет текущим cursor и revision. Route,
который принадлежит application campaign, вместо этого использует cursor
конкретного запуска campaign и не стартует параллельный автономный проход.
Успешная страница атомарно продвигает cursor, частичный сбой повторяет ту же
страницу, а restart восстанавливает незавершённую задачу. Поиск останавливается
по `pages`, пустой странице или пределу HH в 2000 результатов. SQLite и
in-memory repository хранят одну вакансию на `(platform, external_id)` и один
отклик на `(profile, vacancy)`; задачи дедуплицируются по стабильным
idempotency keys.

HH adapter отправляет OAuth Bearer token и `HH-User-Agent`, использует
`per_page=100`, нормализует краткую вакансию и строго отклоняет неизвестные либо
структурно некорректные search-поля. `429 Retry-After` переводит page task в
отложенный retry и освобождает worker lease; ожидание не выполняется внутри
handler. Пока этим транспортом реализован только `source=global`.

Для постоянного состояния доступен SQLite store: он реализует те же
repository/broker-порты и сохраняет vacancies, discoveries, applications,
application campaign с route cursor и связями на найденные отклики, а также
idempotent tasks в одном файле. Campaign definition неизменяема, продвижение
защищено revision CAS; фактические результаты считаются по связанным
applications и их durable tasks без дублирующих изменяемых счётчиков.
In-memory реализации остаются для быстрых unit-тестов. Путь задаётся через
`database.path`.

Миграции не выполняются при старте основного сервиса. Отдельный entrypoint на
`golang-migrate` применяет embedded versioned SQL; после этого `job-agent`
проверяет точную schema version и отказывается работать с отсутствующей,
устаревшей или dirty-схемой:

```sh
go run ./cmd/job-agent-migrate -config ./config/example/config.json up
go run ./cmd/job-agent ./config/example/config.json
```

Вторая команда запускает долгоживущий API на `http://127.0.0.1:8080` и
периодический reconcile follow-up таймеров. До появления API-аутентификации
конфигурация намеренно запрещает bind не на loopback-интерфейс. Адрес и период
задаются через `server.listen` и `server.follow_up_reconcile_interval`.

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

Conversation workflow планирует таймеры, идемпотентно ставит due-задачи и перед
отправкой повторно проверяет входящие сообщения, статус диалога, лимит и
cooldown. Минимальный `net/http` API предоставляет чтение диалогов/messages,
send, sync, mark-read и CRUD/run для follow-up с `Idempotency-Key` и revision
CAS через `If-Match`.

Consumer очереди атомарно получает задачу вместе с ограниченным lease и
случайным token. Поддерживаются heartbeat/extend, complete, delayed retry и
terminal fail. После падения worker задача повторно выдаётся по окончании lease,
а запоздалый результат старого worker отклоняется.

Основной `application.submit` worker ведёт отклик через состояния
`new → preparing → ready → submitting → submitted`. Выбор резюме задаётся для
профиля полем `resume`. До решения worker авторизованно читает полную вакансию
через `GET /vacancies/{id}` и обновляет её в repository: краткой поисковой
карточки недостаточно для требований и applicant relations. Закрытая вакансия
или уже существующий `got_response` пропускаются без `POST`; обязательный тест
либо отсутствующее обязательное сопроводительное переводят отклик в
`waiting_validation`. Для выбранного `resume` worker также читает все страницы
`GET /vacancies/{id}/suitable_resumes`; если резюме отсутствует в ответе,
отклик остаётся на ручную проверку. Подтверждённый `resume_id`, результат
решения и готовое письмо сохраняются вместе до внешнего действия, поэтому
изменение конфига не меняет уже подготовленный retry. Перед
`POST /negotiations` SQLite атомарно
резервирует слот дневного бюджета профиля и платформы. Успех фиксирует слот,
гарантированный отказ освобождает его, а потерянный ответ оставляет отклик в
`pending_reconciliation`, не повторяя небезопасный POST.

Политика выполнения задаётся в `profile.applications`. Без явной настройки
используется безопасный `dry_run`; `approval` останавливает отклик в
`waiting_approval`; `submit` требует положительный `daily_limit`. Граница суток
считается в заданной `timezone`.

Детерминированный application operator проверяет `include_any`/`exclude_any`
по названию, работодателю, описанию и ключевым навыкам полной вакансии. Границы
слов учитываются, поэтому термин `Go` не совпадает с `Django`. Статическое
письмо задаётся через `message`, а шаблон — через `message_template`; одновременно
их включать нельзя. Шаблону доступны `ProfileID`, `Title`, `Employer`, `URL`,
`Description` и `KeySkills`. Результат решения (`decision_code`, причина и
готовый текст) сохраняется в `Application` до внешнего действия и повторно
используется после retry вместе с выбранным резюме.

Пример безопасной настройки:

```json
"applications": {
  "mode": "dry_run",
  "message_template": "Здравствуйте, {{.Employer}}! Меня заинтересовала вакансия {{.Title}}.",
  "qualification": {
    "include_any": ["Go", "Golang", "Backend"],
    "exclude_any": ["директор", "руководитель направления"]
  },
  "daily_limit": 20,
  "timezone": "Europe/Moscow"
}
```

HH-адаптер отправляет multipart-отклик через официальный API. Локальный
idempotency key не передаётся HH, потому что endpoint не предоставляет такого
параметра: он дедуплицирует durable task и пару profile/vacancy. Ответ
`already_applied` считается идемпотентным успехом. После неоднозначного исхода
worker читает `relations` полной вакансии: `got_response` подтверждает отклик,
его отсутствие завершает application как неотправленный и освобождает бюджет.
Запись каждого перехода защищена сравнением ожидаемого статуса, поэтому два
worker не могут одновременно завершить один отклик.

`application.campaign` — короткий повторяемый workflow, а не один длинный
handler. Каждый tick читает одну страницу route либо сверяет уже созданные
отклики, сохраняет revision и ставит следующий tick с новым idempotency key.
Вся страница сохраняется как campaign items, но одновременно в
`application.submit` выдаётся не больше `max_in_flight` задач; оставшиеся items
остаются `planned`, поэтому хвост страницы не теряется. После завершения
активных откликов campaign либо продолжает cursor, либо переходит к следующему
route. Она останавливается на `target_successful` или после исчерпания всех
fallback routes. Сбой после сохранения cursor, но до enqueue следующего tick,
восстанавливается повтором старой задачи без повторного продвижения страницы.
Если `application.submit` уже отложена по `Retry-After`, campaign также ставит
следующую проверку на её `available_at`, а не создаёт revision каждые несколько
секунд до сброса квоты.

Пример action внутри cron job:

```json
"action": {
  "type": "application.campaign",
  "profiles": ["primary", "secondary"],
  "routes": ["golang-primary", "backend-fallback"],
  "target_successful": 20,
  "max_in_flight": 3
}
```

Campaign routes должны использовать один adapter и включать все указанные
профили. Пока профиль не авторизован, соответствующий job не регистрируется.

Поднятие резюме — второй core-контур. Декларативные jobs задают cron expression,
timezone, `misfire: run_once` и bounded jitter, а встроенный scheduler хранит
`next_run_at` в SQLite и создаёт обычные durable `resume.touch` или
`application.campaign` tasks. После простоя пропущенные интервалы схлопываются
в один запуск. Jitter записывается в `available_at`, поэтому worker не удерживает
lease во время ожидания. Реальный HH transport читает
`canTouch`/`nextTouchAt` из server-rendered profile state и возвращает
`Retry-After`; четырёхчасовой интервал не зашит в core. Первая публикация
(`resume.publish`) остаётся отдельной операцией.
Как и для откликов, scheduler не активирует job профиля до регистрации рабочего
transport, чтобы не копить заведомо невыполнимые действия.

Worker runtime запускает отдельный type-filtered consumer для
`conversation.send`, `conversation.follow_up`, `conversation.mark_read` и
`conversation.sync`. Handler передаёт transport-у task idempotency key,
сохраняет нормализованный результат и завершает follow-up только после записи
исходящего сообщения. Временные, rate-limit и auth/confirmation ошибки получают
bounded retry; unsupported и permanent ошибки завершают задачу terminal fail.

Fallback между transport-реализациями разрешён только для операции, которую
текущий transport не поддерживает. Ошибки авторизации, валидации, rate limit и
временные сбои не маскируются переключением на другой transport.

HH-адаптер умеет проверять собственную поисковую конфигурацию и выполнять
авторизованный read-probe профиля через официальный `GET /me`. Профиль с OAuth
токеном ссылается на отдельный credential-файл через `credentials_ref`; токен
не хранится в основном config и не попадает в диагностические ошибки. Проверки
одного профиля сериализуются, а `401/403` переводят профиль в
`auth_required` и не запускают его workers. Conversation transport пока
возвращает явный `unsupported`; application transport и его reconciliation
работают через официальный HH API.

Минимальный credential-файл имеет права `0600` (или строже):

```json
{"access_token":"..."}
```

В профиле указывается только ссылка:

```json
{
  "tag": "primary",
  "adapter": "hh-main",
  "credentials_ref": "file:/run/secrets/hh-primary.json",
  "resume": "replace-with-hh-resume-id",
  "applications": {"mode": "dry_run", "timezone": "Europe/Moscow"},
  "enabled": true
}
```

Импорт готового токена уже работает; OAuth login и обновление access token по
refresh token остаются отдельными следующими срезами. Browser `state_file`
по-прежнему используется независимо для browser-only операций вроде поднятия
резюме.

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
