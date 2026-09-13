# Возможности и runtime-детали

Подробный обзор реализованного поведения: режимы откликов, адаптеры, поиск и
ранжирование, operators, анкеты и тесты, чаты, dashboard, конфигурация и
наблюдаемость. Это справочный материал — начните с
[`quickstart.md`](quickstart.md), а сюда возвращайтесь за деталями.

## Docker Compose: детали

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

Для реального профиля создайте отдельный каталог конфигурации вне Git,
положите туда основной JSON и все файлы, на которые он ссылается относительными
путями (`messages/`, resume facts и bootstrap manifests). Передайте каталог
через `JOB_AGENT_CONFIG_DIR`, а имя основного файла — через
`JOB_AGENT_CONFIG_NAME`. В контейнере весь bundle доступен read-only в
`/config`, тогда как SQLite и browser state остаются в writable `/data`.
Runtime image имеет фиксированный рабочий каталог `/`, поэтому существующие
пути `./data/...` однозначно попадают в этот volume и не зависят от домашнего
каталога пользователя базового image.
Compose ограниченно переопределяет только listener backend через
`JOB_AGENT_SERVER_LISTEN=0.0.0.0:8080` и
`JOB_AGENT_SERVER_EXPOSURE=private`: это позволяет dashboard обращаться к API
по внутренней сети, не публикуя backend-порт на хост. Остальная конфигурация
остаётся декларативной и читается из bundle.
Backend по умолчанию запускается как `1000:100`; при другом владельце каталога
данных задайте `JOB_AGENT_UID` и `JOB_AGENT_GID`. Root для нормального запуска
не требуется.

Например, локальный bundle в `./data` с файлом `config.local.json` запускается
так:

```sh
JOB_AGENT_CONFIG_DIR=./data \
JOB_AGENT_CONFIG_NAME=config.local.json \
  docker compose up -d --build
```

Dashboard по умолчанию доступен только на `127.0.0.1:8081`. Для доступа через
WireGuard укажите точный адрес tunnel-интерфейса хоста, например:

```sh
JOB_AGENT_DASHBOARD_BIND_IP=10.66.66.1 \
  docker compose up -d --build
```

Не используйте `0.0.0.0`: пользовательская аутентификация API ещё не
реализована. Backend не публикуется на host и доступен dashboard только через
Compose network. Устройство dashboard, очередей и будущего общего browser
service описано в [`dashboard-runtime.md`](dashboard-runtime.md).

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
- versioned one-shot bootstrap профиля/резюме из JSON с redacted plan,
  compare-before-write, read-back и повторным `no_changes`.

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
отложенный retry и освобождает worker lease. Реализованы `source=global`,
`similar_resume`, `similar_vacancy` и `related_vacancy`; API-канал исполняет все
четыре, browser-канал — `global` и `similar_resume` через
`/search/vacancy?resume=`. Новые employment/work-поля принимаются только для
`global`, а `related_vacancy` допускает только пагинацию.

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
- `GET /api/v1/profile-state/resources/{tag}/editor` явно загружает объявленные
  resume-scoped leaves со значением text/`null` (не только `about`) для
  локальной формы; этот endpoint содержит персональный
  текст и остаётся за той же доверенной tunnel-boundary, что и переписки.
  Массивы, объекты, числа и boolean в текстовую форму не попадают, а adapter
  writer проверяет свой allowlist и подтверждает результат read-back;
- `POST /api/v1/profile-state/resources/{tag}/plans` сам читает актуальный HH
  state и сохраняет immutable proposal; опциональный one-shot override требует
  digest базового manifest;
- `POST /api/v1/profile-state/bootstrap/plans` принимает versioned one-shot
  `ProfileBootstrap`, проверяет profile-scoped reader/writer и строит такой же
  immutable redacted plan без регистрации временного runtime resource;
- `POST /api/v1/profile-state/resources/{tag}/reconcile` с `Idempotency-Key`
  ставит durable read-plan-apply task и подходит для dashboard/API trigger;
- `GET /api/v1/profile-state/proposals` и
  `GET /api/v1/profile-state/proposals/{id}` возвращают redacted plans без
  текущего и желаемого текста;
- `POST /api/v1/profile-state/proposals/{id}/apply` явно ставит immutable plan
  в очередь. Task содержит только `proposal_id`, точный snapshot загружается
  worker'ом из SQLite;
- `GET /api/v1/profile-state/revisions` возвращает историю подтверждённых apply:
  before/after digests, изменённые JSON Pointer paths, `source` и `applied_at`,
  без значений полей. Фильтры `resource_tag`, `profile_id` и `limit`
  (1–200, по умолчанию 50).

Dashboard показывает ресурсы, объявленные пути и redacted diff и разделяет
кнопки «Построить план» и «Применить». Команда «Сверить и применить» ставит
асинхронный reconcile, не выполняя HH-запрос внутри HTTP request. Кнопка
«История ревизий» загружает redacted-историю подтверждённых apply по ресурсу.
Сейчас HH
Browser writer через текущую cookie-сессию поддерживает allowlisted поля
web-редактора: личные данные, позицию, опыт, key skills, «О себе», форматы и
образование. OAuth writer дополнительно поддерживает native `resume_profile`
секции. При стороннем изменении после plan PUT/POST не выполняется, а задача
завершается конфликтом; повтор после потерянного ответа сначала проверяет
фактическое состояние.

Для заполнения профиля и резюме из одного файла используется CLI-клиент:

```bash
job-agent startup --api http://127.0.0.1:8081 ./data/resume.json
job-agent profile bootstrap --api http://127.0.0.1:8081 ./data/resume.json
job-agent profile bootstrap --api http://127.0.0.1:8081 --apply ./data/resume.json
```

`startup` сразу ставит plan в durable queue; `profile bootstrap` без `--apply`
только показывает операции и пути. Формат, пример с `experience`/`keySkills` и
ограничения описаны в [`profile-bootstrap.md`](profile-bootstrap.md).
Тот же manifest можно подключить к долгоживущему сервису в записи профиля:

```json
"bootstrap": {
  "source": "../../data/primary-resume.json",
  "when": "empty"
}
```

Относительный `source` разрешается от каталога основного config. После
авторизации startup читает только объявленные поля. Bootstrap ставится в
durable apply-очередь, лишь когда все они отсутствуют либо пусты; частично
заполненный профиль целиком пропускается без перезаписи. `missing_resume` и
`publish: true` пока отклоняются конфигом: создание и публикация резюме ещё не
реализованы как отдельные adapter actions.
Задача `profile_state.apply` получает системный максимальный приоритет. Пока
она ожидает, выполняется или остаётся в `failed`, worker не забирает
`application.submit` того же профиля: ожидание не расходует attempts отклика,
а другие профили продолжают работать. Упавший apply можно явно перезапустить
через `POST .../retry` либо признать неактуальным через `POST .../dismiss`;
неявного продолжения со старым состоянием нет. Уже начатый внешний запрос не
прерывается.

SQLite и memory stores также реализуют progressive test catalog и human review
history. Каталог создаётся до начала попытки и пополняется вопросами независимо;
изменённый набор вариантов сохраняется под новым question fingerprint. Review
prompt включает полный runtime-вопрос, а выбор пользователя append-ится вместе
с атомарным CAS по session revision, поэтому повторные REST/TG callbacks не
могут отправить два ответа. `GET /api/v1/review-sessions` возвращает redacted
список сессий (статус, профиль, платформа, текущий вопрос и его тип) для
dashboard: секция «Проверки и опросники» показывает ожидающие сессии, рендерит
single/multiple/text prompt и записывает ответ через тот же durable
`review.answer` с Idempotency-Key.

Неизвестный вопрос qualification может автоматически решить модель:

```json
"answers": {
  "model": {
    "provider": "mini",
    "prompt_version": "answer-fallback-v1",
    "instruction": "Выбери ответ только из предложенных вариантов.",
    "timeout": "30s"
  }
}
```

Провайдер ссылается на top-level `models`. Модель получает только текст вопроса
и показанные варианты; локальный validator принимает ответ лишь при точном
совпадении с вариантом (single/multiple) или корректном ограниченном тексте, а
`code` и contextual-вопросы в модель не уходят. Timeout, rate limit и
invalid output не блокируют попытку: вопрос переходит в обычный review. Ответ,
подтверждённый успешным `QualificationResult`, дописывается в
qualification-блок как verified revision с provenance (model tag, provider
model, prompt version, response ID и digests) и переиспользуется дальше.
Контракт целиком — `docs/next-answer-model-fallback.md`.

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
`waiting_approval`. Для HH режим `submit` по умолчанию получает дневной budget
`200` откликов на профиль и равномерный `submit_jitter` от `15s` до `25s`.
Явный `daily_limit` может только уменьшить этот ceiling; значения выше `200`
отклоняются конфигом. Граница суток считается в заданной `timezone`.

Pacing применяется только непосредственно перед внешним submit. Каждый отклик
получает постоянный слот в SQLite; ожидающая задача освобождает worker lease и
возвращается в очередь до `scheduled_at`. После перезапуска просроченные слоты
перестраиваются, поэтому накопившиеся отклики не отправляются burst-пакетом.
При необходимости диапазон можно сузить явно:

```json
"submit_jitter": {"min": "18s", "max": "22s"}
```

Детерминированный application operator проверяет `include_any`/`exclude_any`
по названию, работодателю, описанию и ключевым навыкам полной вакансии. Границы
слов учитываются, поэтому термин `Go` не совпадает с `Django`. Статическое
письмо задаётся через `message`, а именованный пул шаблонов — предпочтительно
через отдельный [`message_template_file`](reference/messages.md); имя файла
без расширения становится tag пула. Файл поддерживает стратегии `first` и воспроизводимую `stable_hash`
по profile/vacancy. Старый формат с единственным полем `template` и встроенный
`message_template` сохранены для совместимости. Одновременно разрешён только
один источник. Шаблону доступны
`ApplicationID`, `ProfileID` и вложенный объект `Vacancy` с идентификатором,
platform, состоянием, датой публикации, описанием, ключевыми навыками и
нормализованными adapter attributes. Старые плоские `Title`, `Employer`, `URL`,
`Description` и `KeySkills` сохранены для совместимости. Поэтому шаблон и
будущий model operator могут использовать один структурированный контекст
полной вакансии. Результат решения (`decision_code`, причина с tag
пула/варианта и готовый текст) сохраняется в `Application` до внешнего действия
и повторно используется после retry вместе с выбранным резюме. Поэтому
изменение порядка или содержимого пула не меняет уже подготовленный отклик.
Provenance хранит SHA-256 digest нормализованного содержимого всего выбранного
пула (tag, strategy и templates), поэтому можно отличить две ревизии одного
файла, но форматирование самого JSON не создаёт ложную новую ревизию.

Переиспользуемые `employer_groups` сопоставляют вакансию по точному
platform-specific `employer_id`, точному нормализованному имени-алиасу либо
через вложенную группу. Нечёткое и substring-сопоставление намеренно не
используются. Упорядоченные `applications.employer_rules` применяют первое
совпавшее правило и могут выбрать отдельный `message_pool`, пропустить вакансию
(`skip`) или отправить её на ручную проверку (`review`). Причина решения
содержит группу и тип доказательства совпадения.

Application model подключается как именованный top-level provider и остаётся
сменяемым относительно operator pipeline. Реализованы `openai_responses` через
`POST /responses` и `openai_chat` через chat completions (DeepSeek и другие
OpenAI-совместимые endpoint-ы, structured output через JSON mode): API key
читается только из указанной переменной окружения, структурированный vacancy
context передаётся как данные, а Responses-запрос всегда задаёт `store:false`. Profile-level `model` либо employer rule с action `model`
обязательно имеют готовое письмо или файловый пул как fallback. Timeout,
rate-limit, временная ошибка, пустой или слишком длинный ответ не блокируют
campaign: operator выбирает fallback и сохраняет в причине provider tag,
категорию ошибки и выбранный вариант. Отмена родительской task не маскируется
fallback. Успешный сгенерированный текст сохраняется в `Application`, поэтому
retry не вызывает модель повторно. Перед сохранением модельный текст также
отклоняется при наличии JSON/code fence, незаполненных `{{placeholders}}`,
неподдерживаемых управляющих символов и чисел, URL или e-mail, которых не было
в структурированном контексте.

Provider также обязан вернуть строгий structured output: итоговый `text` и
непустой список evidence claims. Каждый claim является точным уникальным
фрагментом письма; claims покрывают весь буквенно-цифровой текст. Источники
задаются RFC 6901 JSON Pointer только в `resume.facts` либо разрешённые поля
`vacancy`, а quote должен буквально присутствовать и в исходном scalar, и в
claim. Числа, URL и e-mail проверяются внутри того же claim, а не против всего
контекста. Любая отсутствующая, неоднозначная или неверная ссылка превращает
ответ в `invalid_output` и включает настроенный fallback. Это проверяет
заявленную моделью привязку текста к источникам, но не доказывает логическую
истинность произвольной естественно-языковой формулировки.

SQLite отдельно сохраняет versioned `preparation_provenance`: фактический
источник текста, provider/operator tag, prompt version, выбранную модель,
provider response ID, tag/digest resume facts, input/output/evidence digests,
число evidence claims и, для pool/fallback, digest пула, безопасную категорию
ошибки и выбранный pool/template. Полный prompt,
исходные facts, ответ с reasoning и текст ошибки провайдера в provenance не
копируются. Новые записи используют provenance v3; v1 и v2 остаются читаемыми.
Миграция 14 добавляет поле старым applications как пустой объект, не меняя уже
сохранённые решения.

План и оставшиеся критерии готовности описаны в
[`next-cover-letter-routing.md`](next-cover-letter-routing.md).

Формат пула:

```json
{
  "strategy": "stable_hash",
  "templates": [
    {"tag": "concise", "template": "Здравствуйте! {{.Vacancy.Title}}"},
    {"tag": "project-focus", "template": "Здравствуйте, {{.Vacancy.Employer}}! ..."}
  ]
}
```

Минимальная модель с обязательными fallback и фактами выбранного резюме
(показаны соответствующие фрагменты общего config):

```json
"models": [
  {
    "tag": "cover-letter-mini",
    "type": "openai_responses",
    "model": "gpt-5.4-mini",
    "api_key_env": "OPENAI_API_KEY",
    "max_output_tokens": 1024
  }
],
"profiles": [
  {
    "tag": "primary",
    "adapter": "hh-main",
    "resume": "resume-id-from-platform",
    "resume_facts_file": "resumes/backend.json",
    "enabled": true,
    "applications": {
      "message_template_file": "messages/backend.json",
      "model": {
        "provider": "cover-letter-mini",
        "prompt_version": "backend-v1",
        "instruction": "Напиши краткое предметное сопроводительное без общих фраз.",
        "timeout": "20s"
      }
    }
  }
]
```

`resumes/backend.json` содержит только факты, которые разрешено сообщать
работодателю:

```json
{
  "resume_id": "resume-id-from-platform",
  "facts": {
    "headline": "Backend developer",
    "skills": ["Go", "PostgreSQL"],
    "commercial_years": 3,
    "work_format": "удалённо"
  }
}
```

Неизвестные поля верхнего уровня, `null`, пустые строки, чрезмерная вложенность
и файлы больше 128 KiB отклоняются. `resume_id` обязан совпадать с
`profiles[].resume`. Loader вычисляет digest нормализованного содержимого;
путь и полный файл не попадают в причину решения.

При Compose-запуске `OPENAI_API_KEY` передаётся только backend-сервису. Для
другого `api_key_env` переменную нужно явно добавить в deployment override.
`job-agent-check` проверяет наличие секрета, но не печатает значение и не делает
платный model request.

Пример безопасной настройки:

```json
"employer_groups": [
  {
    "tag": "marketplaces",
    "rules": [
      {"platform": "hh", "employer_id": "2180"},
      {"name": "Ozon Tech"}
    ]
  }
],
"applications": {
  "mode": "dry_run",
  "message_template_file": "messages/backend.json",
  "employer_rules": [
    {
      "employer_groups": ["marketplaces"],
      "action": "message_pool",
      "message_template_file": "messages/marketplace.json"
    }
  ],
  "allow_visibility_change": false,
  "qualification": {
    "include_any": ["Go", "Golang", "Backend"],
    "exclude_any": ["директор", "руководитель направления"]
  },
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
Вся страница сохраняется как campaign items; внутри route кандидаты выдаются
по `published_at DESC` с fallback на время наблюдения, поэтому сначала
обрабатываются самые свежие вакансии. Но одновременно в
`application.submit` выдаётся не больше `max_in_flight` задач; оставшиеся items
остаются `planned`, поэтому хвост страницы не теряется. После завершения
активных откликов campaign либо продолжает cursor, либо переходит к следующему
route. Она останавливается на `target_successful` или после исчерпания всех
fallback routes. Сбой после сохранения cursor, но до enqueue следующего tick,
восстанавливается повтором старой задачи без повторного продвижения страницы.

`limit_exceeded` переносит готовую задачу на начало следующего настроенного
дневного окна, если HH не сообщил точный `Retry-After`; ответ платформы всегда
имеет приоритет над локальным fallback. [Официальный HH error
contract](https://github.com/hhru/api/blob/master/docs/errors.md#переписка-откликиприглашения)
описывает сам `negotiations/limit_exceeded`, но не обещает время reset, поэтому
граница задаётся `profile.applications.timezone`, а не зашивается в adapter.

Временная подстройка резюме перед одним откликом и точный restore реализованы
как durable saga с per-profile lease; политика включается
`applications.tailoring.skills`, опционально с model processor и fallback на
детерминированную политику. Условия и ограничения — в
[application-resume-tailoring.md](application-resume-tailoring.md).
Если `application.submit` уже отложена по `Retry-After`, campaign также ставит
следующую проверку на её `available_at`, а не создаёт revision каждые несколько
секунд до сброса квоты.

Dashboard API возвращает последние 20 campaigns отдельно от глобальных
счётчиков: job tag, terminal/running status, цель, текущий route и
сгруппированные application outcomes конкретного запуска. Содержимое вакансий,
сопроводительных, task payload и внешние vacancy ID в эту сводку не попадают.

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
`resume.publish`, `resume.update`, `profile.activity.observe`,
`profile.session_refresh`,
`conversation.follow_up.select`, `profile_state.reconcile` или
`application.campaign` tasks. После простоя пропущенные интервалы схлопываются
в один запуск. Jitter записывается в `available_at`, поэтому worker не удерживает
lease во время ожидания. Реальный HH transport читает
`canTouch`/`nextTouchAt` из server-rendered profile state и возвращает
`Retry-After`; четырёхчасовой интервал не зашит в core. Публикация
(`resume.publish`) выполняется через applicant API `POST /resumes/{id}/publish`
для OAuth-профилей: успех возвращает `204`, а ранняя попытка с
`next_publish_at` нормализуется в `rate_limited` с `Retry-After`. Для профиля
только с browser-cookie publish возвращает `Unsupported`.
Как и для откликов, scheduler не активирует job профиля до регистрации рабочего
transport, чтобы не копить заведомо невыполнимые действия.

Action
`{"type":"resume.update","profile":"primary","resource":"<tag>","publish":false}`
по расписанию сверяет объявленный desired state резюме с платформой:
plan → apply → read-back, а при `publish: true` дополнительно публикует резюме.
Подтверждение для планового обновления не требуется. Если платформа ещё не
разрешает операцию (cooldown, `Retry-After`, `next_publish_at`), durable task не
завершается с ошибкой, а перепланируется на первый разрешённый момент; пока
задача активна, scheduler не создаёт по ней дубликат. `resume`, если не задан,
берётся из профиля; при `publish: true` он обязателен, а профиль без
зарегистрированного publisher job просто не активирует.

Job может задать числовой `priority` в диапазоне от `-1000` до `1000`; по
умолчанию используется `0`. Worker сначала выбирает больший priority, затем
сохраняет FIFO-порядок по `available_at`, `created_at` и ID. Приоритет сравним
только внутри одного type-filtered consumer и не прерывает уже выполняемую
задачу. Campaign tick, созданные им `application.submit`, а также цепочка
страниц обычного search наследуют исходный priority. SQLite сохраняет это поле,
поэтому retry и restart не меняют порядок. Dashboard группирует очередь в том
числе по priority.

Read-only action `profile.activity.observe` периодически сохраняет доступные в
HH profile initial state показатели резюме: показы, просмотры, новые просмотры,
приглашения и текущий response streak. Если HH скрывает activity UI
feature-флагом, snapshot помечается `score_hidden`, но неизвестный score не
вычисляется локально. Отдельный идемпотентный журнал сопоставляет этим снимкам
реальные `vacancy.inspected`, `application.submitted`,
`conversation.message_sent` и `resume.touched`.

Периодический `conversation.follow_up.select` выбирает не произвольный чат, а
активный диалог, в котором последнее содержательное сообщение было исходящим и
после него работодатель не ответил. Доступны стратегии `oldest_unanswered`,
`newest_unanswered` и воспроизводимая для одного task `random`. Minimum silence,
cooldown, deadline и максимальное число напоминаний обязательны; входящий ответ
до отправки отменяет follow-up. Сам выбор реализован platform-neutral. Для
browser-backed HH-профиля изменение чатов дополнительно открывается явной
политикой `conversations.allow_send`; `allow_mark_read` управляет независимой
операцией чтения. Оба разрешения по умолчанию выключены.

Политика `conversations.answer_known` (требует `allow_send`) разрешает
автоматически отвечать на пришедший в чат questionnaire reviewed-блоком из
`answer_sets`: text-button HH подтверждён живьём как обычное сообщение с точным
текстом кнопки. Ответ ставится durable задачей, идемпотентной по
`(chat, prompt message, option)`; неизвестный, неоднозначный или устаревший
вариант не отправляется и остаётся человеку.

Изменяющие профиль `resume.touch`, `profile_state.apply` и фактическая отправка
`application.submit` сериализуются общей process-local lane по `profile_id`:
операции одного профиля не пересекаются, а разные профили продолжают работать
параллельно. Текущий Compose запускает один backend; для нескольких
mutating-реплик потребуется shared lease в хранилище.

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
`conversation.send`, `conversation.follow_up`, `conversation.follow_up.select`,
`conversation.discover`, `conversation.mark_read` и `conversation.sync`.

Dashboard отдельно показывает terminal failed-задачи без command payload,
source, idempotency key и correlation data. Для каждой доступны только явные
операторские действия:

```text
GET  /api/v1/tasks/failed
POST /api/v1/tasks/{task_id}/retry
POST /api/v1/tasks/{task_id}/dismiss
```

`retry` начинает новый bounded attempt cycle и может повторить внешний side
effect, поэтому UI требует отдельного подтверждения. `dismiss` не удаляет
историю и причину failure, а переводит устаревшую задачу в terminal
`dismissed`. `job-agent-check` выводит безопасные сведения о failure и помечает
runtime как `degraded`; failed `profile_state.apply`, который блокирует
отклики профиля, повышает результат до `blocked`.
Плановый action `conversation.sync` сначала читает каталог HH, идемпотентно
создаёт локальные диалоги, а затем ставит отдельную полную sync-задачу для
каждого диалога. Handler передаёт transport-у task idempotency key,
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
`auth_required`. Browser conversation transport читает `chatik` catalog/history
и, только при включённых profile-policy, отправляет текст или помечает чат
прочитанным. Application transport и reconciliation предпочитают официальный HH API, а без
OAuth работают через отдельно привязанный browser transport. Само наличие
browser state по-прежнему даёт только чтение: write transport создаётся
composition root только для режимов `approval` и `submit`.

Минимальный credential-файл имеет права `0600` (или строже):

```json
{"access_token":"..."}
```

Поддерживаются две схемы `credentials_ref`: `file:` для JSON и
`dotenv-file:` для dotenv с `HH_ACCESS_TOKEN`, `HH_REFRESH_TOKEN` и
`HH_EXPIRES_AT`. Bare path без схемы по-прежнему читается как JSON. Loader
принимает только regular file с правами без group/other. Пакет `credentials/`
уже умеет писать запись атомарно, выставляет `0600`, не следует по symlink и
не перезаписывает существующий secret без явного `force`.

Интерактивный вход выполняется через локальный backend и включается, когда
заданы `BROWSER_WORKER_URL` и `BROWSER_WORKER_TOKEN` (Compose-профиль
`browser`). CLI входит по email и одноразовому коду, рисует captcha в терминале
и сохраняет санитизированный browser storage state:

```sh
job-agent auth login \
  --api http://127.0.0.1:8080 \
  --profile primary \
  --state-output ./data/profiles/primary.json
job-agent auth status --api http://127.0.0.1:8080 --session <id>
job-agent auth import \
  --source ./exported-storage-state.json \
  --state-output ./data/profiles/primary.json
```

`auth import` принимает полный Playwright export, оставляет только домены
`hh.ru`/`hhcdn.ru` и пишет файл атомарно с `0600`; существующий файл
заменяется только с `--force`.

Парольная ветка намеренно не автоматизируется, а OAuth-токены записываются
через `--credential-output`, когда обмен доступен для аккаунта.

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

Заблокированный или упавший отклик можно явно освободить после исправления
причины (например, после смены resume hash или заполнения анкеты):

```text
POST /api/v1/applications/{application_id}/retry
Idempotency-Key: <request key>
```

Действие очищает записанное решение и записанную ошибку, возвращает отклик в
`ready`, после чего обычный durable `application.submit` заново читает вакансию
и повторяет preflight. Повтор с тем же ключом возвращает существующую задачу;
отклик в другом статусе отклоняется. В dashboard кнопка «Повторить» есть у строк
`waiting_validation` и `failed`.

`job-agent-trigger` ставит одну включённую job в ту же durable-очередь, не
выполняя внешнее действие внутри CLI. Обязательный `-idempotency-key` делает
повтор команды безопасным: тот же ключ возвращает уже созданную задачу, а
другой payload под тем же ключом отклоняется. После успешного preflight можно
поставить job вручную и запустить обычный `job-agent`; cron при этом остаётся
единственным владельцем периодического расписания.

Запущенный backend предоставляет тот же control-plane сценарий без доступа к
SQLite из отдельного процесса:

```text
GET  /api/v1/jobs
POST /api/v1/jobs/{job_tag}/runs
Idempotency-Key: <client request id>
```

В списке присутствуют только jobs, для которых при старте реально собраны
transport, авторизация и capability. Ручной запуск создаёт обычную durable task
с теми же payload и priority, что cron, и не выполняет platform action в HTTP
handler. Dashboard показывает этот список и ставит выбранную job в очередь.

`job-agent-approve` выпускает один уже подготовленный `waiting_approval`
отклик. Команда работает только после явного переключения профиля в `submit`,
требует непустое сохранённое письмо и ставит idempotent submit-task. Если
процесс оборвался между сменой статуса и постановкой задачи, повтор с тем же
ключом безопасно восстанавливает очередь.

Исследованные контракты первого HH-адаптера находятся в `docs/`: public API,
авторизация, global/similar search, chatik, applicant browser operations и
resume/profile schema. REST и automation-контракт чатов описан в
`docs/conversation-api.md`. Главная архитектурная спецификация — [AGENTS.md](https://github.com/Darkon13/job-agent/blob/main/AGENTS.md).

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
