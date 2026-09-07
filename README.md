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

## Docker Compose и dashboard

Backend и опциональный dashboard запускаются разными командами из одного
минимального runtime image. В нём лежат три бинарника, а
build toolchain остаётся только в промежуточном слое multi-stage `Dockerfile`:

```sh
mkdir -p data
chmod 0770 data

# backend без UI; миграции применяются до запуска процесса
docker compose up -d --build job-agent

# backend и отдельный dashboard service на loopback
docker compose up -d --build
```

Для реального профиля скопируйте `deploy/config.example.json` вне Git,
настройте profiles/searches/jobs и передайте путь через
`JOB_AGENT_CONFIG_FILE`. Backend и migrator по умолчанию запускаются как
`1000:100`; при другом владельце каталога данных задайте `JOB_AGENT_UID` и
`JOB_AGENT_GID`. Root для нормального запуска не требуется.

Dashboard по умолчанию доступен только на `127.0.0.1:8081`. Для доступа через
WireGuard/WireGuard укажите точный адрес tunnel-интерфейса хоста, например:

```sh
JOB_AGENT_DASHBOARD_BIND_IP=10.66.66.1 \
  docker compose up -d --build
```

Не используйте `0.0.0.0`: пользовательская аутентификация API ещё не
реализована. Backend не публикуется на host и доступен dashboard только через
Compose network. Устройство dashboard, очередей и будущего общего browser
service описано в [`docs/dashboard-runtime.md`](docs/dashboard-runtime.md).

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
по `pages`, пустой странице, локальному `max_pages` или пределу HH в 2000
результатов. `page_size` ограничивает число нормализованных карточек одной
страницы. SQLite и
in-memory repository хранят одну вакансию на `(platform, external_id)` и один
отклик на `(profile, vacancy)`; задачи дедуплицируются по стабильным
idempotency keys.

HH adapter выбирает один из двух read-каналов. При наличии OAuth Bearer token
он использует официальный JSON API. Без токена, но с `state_file`, профиль в
режиме `dry_run` читает server-rendered web search и страницы вакансий через
сохранённую browser-сессию. Browser fallback реализует только GET и не
регистрируется как `ApplicationTransport`, поэтому физически не может отправить
отклик. Оба канала нормализуют вакансию и строго отклоняют неизвестные либо
структурно некорректные search-поля. `429 Retry-After` переводит page task в
отложенный retry и освобождает worker lease. Пока реализован только
`source=global`.

Для постоянного состояния доступен SQLite store: он реализует те же
repository/broker-порты и сохраняет vacancies, discoveries, applications,
application campaign с route cursor и связями на найденные отклики, а также
idempotent tasks в одном файле. Campaign definition неизменяема, продвижение
защищено revision CAS; фактические результаты считаются по связанным
applications и их durable tasks без дублирующих изменяемых счётчиков.
In-memory реализации остаются для быстрых unit-тестов. Путь задаётся через
`database.path`.

При обычном CLI-запуске миграции не выполняются автоматически. Отдельный
entrypoint на `golang-migrate` применяет embedded versioned SQL; после этого
`job-agent` проверяет точную schema version и отказывается работать с
отсутствующей, устаревшей или dirty-схемой. Compose явно запускает backend с
`-migrate-up`, поэтому контейнер сначала применяет ожидающие миграции:

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

Декларативные `resources` типа `profile_state` описывают желаемые поля профиля
и резюме с ownership `declared_fields`: отсутствующее поле не управляется,
`null` означает явную очистку, а массив заменяется целиком. Core строит
канонический JSON, SHA-256 digests и semantic diff только по объявленным
путям. Proposal сохраняется в SQLite без старых/новых значений в публичном
JSON; точный desired snapshot остаётся внутренним, чтобы изменение конфига не
меняло уже построенный план. Доверенный browser reader умеет читать поле
`about` по внешнему ID резюме из уже привязанной HH-сессии. Он выполняет только
`GET`, а неподдерживаемый путь отклоняет целиком до запроса. Явный apply
ставится в durable queue, повторно сверяет before/after digests, отправляет
узкий HH update и считается успешным только после read-back. Наличие resource
при старте ничего само по себе не изменяет.

```json
"resources": [
  {
    "tag": "primary-backend",
    "type": "profile_state",
    "profile": "primary",
    "ownership": "declared_fields",
    "state": {
      "resumes": {
        "replace-with-hh-resume-id": {"about": "Итоговый текст раздела «О себе»"}
      }
    }
  }
]
```

Read/plan API не принимает observation от клиента:

- `GET /api/v1/profile-state/resources` возвращает только metadata, объявленные
  JSON Pointer paths и доступность доверенного reader;
- `GET /api/v1/profile-state/resources/{tag}/editor` явно загружает разрешённые
  desired-значения для локальной формы; этот endpoint содержит персональный
  текст и остаётся за той же доверенной tunnel-boundary, что и переписки;
- `POST /api/v1/profile-state/resources/{tag}/plans` сам читает актуальный HH
  state и сохраняет immutable proposal; опциональный one-shot override требует
  digest базового manifest;
- `POST /api/v1/profile-state/resources/{tag}/reconcile` с `Idempotency-Key`
  ставит durable read-plan-apply task и подходит для dashboard/API trigger;
- `GET /api/v1/profile-state/proposals` и
  `GET /api/v1/profile-state/proposals/{id}` возвращают redacted plans без
  текущего и желаемого текста;
- `POST /api/v1/profile-state/proposals/{id}/apply` явно ставит immutable plan
  в очередь. Task содержит только `proposal_id`, точный snapshot загружается
  worker'ом из SQLite.

Dashboard показывает ресурсы, объявленные пути и redacted diff и разделяет
кнопки «Построить план» и «Применить». Команда «Сверить и применить» ставит
асинхронный reconcile, не выполняя HH-запрос внутри HTTP request. Сейчас HH
browser writer поддерживает только `about`: строка обновляет поле, `null`
очищает его. При стороннем изменении после plan POST не выполняется, а задача
завершается конфликтом; повтор после потерянного ответа сначала проверяет
фактическое состояние.

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
профиля полем `resume`. До решения worker читает полную вакансию через API либо
browser GET и обновляет её в repository: краткой поисковой карточки недостаточно
для требований. Applicant relations и подходящие резюме API получает из
публичных endpoints, а browser transport — из безопасного popup preflight.
Закрытая вакансия
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
письмо задаётся через `message`, а шаблон — предпочтительно через отдельный
`message_template_file`; встроенный `message_template` сохранён для простых
конфигураций. Одновременно разрешён только один источник. Шаблону доступны
`ProfileID`, `Title`, `Employer`, `URL`,
`Description` и `KeySkills`. Результат решения (`decision_code`, причина и
готовый текст) сохраняется в `Application` до внешнего действия и повторно
используется после retry вместе с выбранным резюме.

Следующий срез этого контура — именованные пулы готовых сопроводительных,
model operator и переиспользуемые группы работодателей для фильтрации и
маршрутизации. План и критерий готовности описаны в
[`docs/next-cover-letter-routing.md`](docs/next-cover-letter-routing.md).

Пример безопасной настройки:

```json
"applications": {
  "mode": "dry_run",
  "message_template_file": "messages/backend.json",
  "allow_visibility_change": false,
  "qualification": {
    "include_any": ["Go", "Golang", "Backend"],
    "exclude_any": ["директор", "руководитель направления"]
  },
  "daily_limit": 20,
  "timezone": "Europe/Moscow"
}
```

HH-адаптер предпочитает официальный API, когда профиль привязан к OAuth.
Явно привязанный browser transport перед каждым web-откликом выполняет
идемпотентный popup preflight, проверяет выбранное резюме, тест и лимит письма,
затем отправляет multipart с `resume_hash` и готовым `letter`. Изменение
видимости резюме по умолчанию запрещено и требует
`allow_visibility_change: true`. CAPTCHA и неизвестные варианты формы не
обходятся и переводят отклик на ручную проверку. Локальный
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
`next_run_at` в SQLite и создаёт обычные durable `resume.touch`,
`profile.activity.observe`, `profile_state.reconcile` или
`application.campaign` tasks. После простоя пропущенные интервалы схлопываются
в один запуск. Jitter записывается в `available_at`, поэтому worker не удерживает
lease во время ожидания. Реальный HH transport читает
`canTouch`/`nextTouchAt` из server-rendered profile state и возвращает
`Retry-After`; четырёхчасовой интервал не зашит в core. Первая публикация
(`resume.publish`) остаётся отдельной операцией.
Как и для откликов, scheduler не активирует job профиля до регистрации рабочего
transport, чтобы не копить заведомо невыполнимые действия.

Read-only action `profile.activity.observe` периодически сохраняет доступные в
HH profile initial state показатели резюме: показы, просмотры, новые просмотры,
приглашения и текущий response streak. Если HH скрывает activity UI
feature-флагом, snapshot помечается `score_hidden`, но неизвестный score не
вычисляется локально. Отдельный идемпотентный журнал сопоставляет этим снимкам
реальные `vacancy.inspected`, `application.submitted`,
`conversation.message_sent` и `resume.touched`.

Изменяющие профиль `resume.touch` и `profile_state.apply` сериализуются общей
process-local lane по `profile_id`: операции одного профиля не пересекаются, а
разные профили продолжают работать параллельно. Текущий Compose запускает один
backend; для нескольких mutating-реплик потребуется shared lease в хранилище.

Dashboard позволяет открыть разрешённое поле `about`, подготовить одноразовый
desired override и провести его через тот же plan/apply workflow. Форма не
перезаписывает конфигурацию: исходный resource остаётся источником истины,
override фиксируется только в immutable proposal и защищён digest базового
manifest от применения устаревшего редактора.

Для автоматической сверки job использует action
`{"type":"profile_state.reconcile","resource":"<tag>"}`. Reconcile task не
содержит desired-текст: worker читает resource, строит или переиспользует
proposal и ставит обычный apply. Совпавшее состояние завершается без внешнего
POST. Тот же job можно вручную поставить через `job-agent-trigger`.

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
`auth_required`. Conversation transport пока возвращает явный `unsupported`.
Application transport и reconciliation предпочитают официальный HH API, а без
OAuth работают через отдельно привязанный browser transport. Само наличие
browser state по-прежнему даёт только чтение: write transport создаётся
composition root только для режимов `approval` и `submit`.

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

Импорт готового токена уже работает. Однако 6 сентября 2026 года форма
регистрации приложения в кабинете HH сообщала, что поддержка API для соискателей
прекращена 15 декабря 2025 года, тогда как публичная OpenAPI-документация всё
ещё описывает соискательский OAuth. Поэтому получение нового client ID не
считается доступным путём запуска, пока HH не уточнит условия. Browser
`state_file` используется для discovery, поднятия резюме и явно включённого
browser application transport. Последний всегда делает безопасный popup GET
перед POST и не является неявным fallback после ошибки OAuth API.

```sh
go test ./...
go run ./cmd/job-agent-migrate -config ./config/example/config.json up
go run ./cmd/job-agent-browser-state sanitize ./data/profiles/primary.json
go run ./cmd/job-agent-check ./config/example/config.json
go run ./cmd/job-agent-trigger -idempotency-key manual-touch-001 ./config/example/config.json touch-primary-resume
go run ./cmd/job-agent-approve -idempotency-key approval-primary-hh-42 ./config/example/config.json primary 42
go run ./cmd/job-agent ./config/example/config.json
```

`job-agent-browser-state sanitize` атомарно оставляет в общем Playwright export
только cookies и origins доменов HH, принудительно выставляет `0600` и выводит
лишь количества записей. Значения cookies не выводятся. Команду нужно выполнять
после каждого ручного обновления `state_file` из общего browser-профиля.

`job-agent-check` — безопасная предпусковая проверка. Она проверяет конфиг,
точную версию схемы и статистику SQLite, структуру browser state, поисковые
фильтры, доступность OAuth-профилей через read-only `GET /me` и browser-сессию
поднятия резюме через read-only GET страницы профиля. Команда не синхронизирует
расписание, не ставит задачи в очередь и не выполняет `POST`.
Если настроенный search или job фактически не может работать, проверка
завершается ошибкой вместо «зелёного» запуска с молча отключённым контуром.
Перед запуском она также показывает только агрегированные типы/статусы задач,
статусы/decision codes откликов и просроченные расписания, не раскрывая payload,
причины решения или внешние идентификаторы. `persisted_due` означает, что
`misfire: run_once` создаст задачу сразу после старта сервиса.

API-операции и browser-сессия профиля считаются независимо. Отсутствие
`credentials_ref` не мешает поднятию резюме, browser-backed search и
application campaign. Для `approval` и `submit` browser write transport
привязывается отдельно; `dry_run` получает только read transport. Тесты,
CAPTCHA, неподходящее резюме, неизвестная форма и запрещённое изменение
видимости останавливаются в `waiting_validation` без POST.

`job-agent-trigger` ставит одну включённую job в ту же durable-очередь, не
выполняя внешнее действие внутри CLI. Обязательный `-idempotency-key` делает
повтор команды безопасным: тот же ключ возвращает уже созданную задачу, а
другой payload под тем же ключом отклоняется. После успешного preflight можно
поставить job вручную и запустить обычный `job-agent`; cron при этом остаётся
единственным владельцем периодического расписания.

`job-agent-approve` выпускает один уже подготовленный `waiting_approval`
отклик. Команда работает только после явного переключения профиля в `submit`,
требует непустое сохранённое письмо и ставит idempotent submit-task. Если
процесс оборвался между сменой статуса и постановкой задачи, повтор с тем же
ключом безопасно восстанавливает очередь.

Исследованные контракты первого HH-адаптера находятся в `docs/`: public API,
авторизация, global/similar search, chatik, applicant browser operations и
resume/profile schema. REST и automation-контракт чатов описан в
`docs/conversation-api.md`. Главная архитектурная спецификация — `AGENTS.md`.

## Questionnaire mock

Локальный mock показывает single- и multiple-choice вопросы, перемешивает
порядок и runtime ID и экспортирует выбранные ответы как переносимый
qualification `AnswerBlock` с fingerprint каждого отдельного вопроса:

```sh
go run ./cmd/questionnaire-mock
```

После запуска открыть `http://127.0.0.1:8090`. Один JSON содержит один небольшой
блок с `tag`, `name`, `kind` и `platform`. Готовый файл можно загрузить через
file input; ответы восстанавливаются по `question_fingerprint` и тексту
варианта, а не по позиции, runtime ID или заранее известному составу всего
теста. Пример draft-блока находится в
`config/example/mock-answer-block.json`.

### Импорт внешнего учебного банка

`job-agent-question-bank-import` преобразует Markdown-файлы с checkbox-вариантами
в отдельные локальные study-bank JSON. Для воспроизводимости импорт требует
точный Git revision и сохраняет repository/path/license в каждом файле:

```sh
git clone https://github.com/Londeren/hh-skill-verifications-quizzes /tmp/hh-quizzes
git -C /tmp/hh-quizzes rev-parse HEAD

go run ./cmd/job-agent-question-bank-import \
  -source /tmp/hh-quizzes \
  -out ./data/question-banks \
  -revision <полный-commit-hash>
```

Каталог `data/` исключён из Git. Импортированный материал сохраняется с
`platform: study` и `verification: external_unverified`: отметка `[x]` во
внешнем репозитории является подсказкой для самостоятельной проверки, а не
доказанным результатом платформы. Вопросы, где опубликован только один ответ
без остальных вариантов, импортируются, но не получают fingerprint и не
участвуют в точном runtime-сопоставлении.

Набор с полными вариантами можно открыть в локальном mock:

```sh
go run ./cmd/questionnaire-mock \
  -study-bank ./data/question-banks/docker/basic.json
```

На `http://127.0.0.1:8090` кнопка «Подставить внешние подсказки» проверяет,
что текст ответа однозначно сопоставляется с перемешанными вариантами и
текущими runtime ID. Никакой запрос к HH и submit теста этот режим не делает.
