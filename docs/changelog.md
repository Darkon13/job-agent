# Изменения

## 0.3.0-dev

- Чаты: фильтр «С открытым опросником» и бейдж «опросник» в списке диалогов;
  summary отдаёт `questionnaire_open` для диалогов, где последний входящий
  опросник ещё не имеет ответа после него.
- Вход в HH из dashboard снова работает: когда клиент не передал путь, backend
  берёт `browser_state_reference` из настроенного `state_file` профиля
  (раньше запрос падал с `400`).
- Дедупликация сообщений чата принимает расхождение reply-связки и
  субмиллисекундную разницу времени, поэтому повторный sync после отправки
  ответа не падает с `message identity conflicts with different content`;
  кнопки опросника больше не отправляют локальный `reply_to_id`.
- Опросник вакансии заполняется одной формой: `GET /review-sessions/{id}` отдаёт
  оставшиеся вопросы, `POST .../answers` принимает `answers[]` и записывает все
  ответы одной durable задачей `review.answer` (по ревизии на вопрос), после
  чего автоматически запускает отправку анкеты. Старый пошаговый путь сохранён
  для сессий без объявленного questionnaire.
- Dashboard UX: вход в HH показывает только текущий шаг (поле кода/капчи,
  «Отправить» и «Отменить» появляются по ходу сессии, профиль блокируется на
  время входа); activity показывает один последний снимок на профиль вместо
  карточек по каждому историческому resume hash; пустой список проверок
  разворачивает секцию на одну колонку, по умолчанию видны все сессии и первая
  выбирается автоматически.
- Dashboard: переключатель аккаунтов в шапке фильтрует отклики, чаты, jobs,
  activity, ошибки и review-сессии по выбранному профилю (по умолчанию «Все
  аккаунты», выбор сохраняется локально); список диалогов ограничен по высоте с
  внутренним скроллом.
- Review-сессии показывают вакансию: название, работодателя и ссылку
  (`GET /api/v1/review-sessions` обогащает ответ по `hh:vacancy:<id>`).
- Живой отклик с анкетой доведён до конца: ответы отправляются тем же
  multipart-запросом `POST /applicant/vacancy_response/popup`, что и отклик
  без теста (обычные поля отклика + task-поля); прежний urlencoded POST на
  страницу HH отклонял с `400`. Живая проверка: отклик на 136921562 принят,
  создан negotiation 5570082491.
- Дрейф HH закрыт: когда popup-JSON не содержит `responseStatus`, preflight
  добирает состояние отклика (резюме, `alreadyApplied`, negotiations,
  visibility) из `HH-Lux-InitialState` HTML-страницы. Живьём: отклик на
  136921562 распознаётся как alreadyApplied, 136408820 без отклика — как
  `questionnaire_required`.
- Browser preflight и submit анкеты распознают resume-видимость: если резюме
  доступно только выбранным работодателям и текущего в списке нет, отклик
  останавливается с `resume_visibility_change_required` вместо непонятного
  `400` от HH.
- Dashboard: список диалогов ограничен по высоте и прокручивается внутри
  панели, а не растягивает страницу на весь каталог HH.
- Живой прогон анкеты вакансии выявил и закрыл три разрыва: browser resume
  preflight с `questionnaire_required` теперь ставит `test.capture`, первая
  анкета платформы без готового блока создаёт review-сессию с первого вопроса,
  а resolver собирается даже при пустом `answer_sets`. Первый ответ человека
  формирует conventional reviewed-блок; живьём создана сессия
  `hh:vacancy:136921562` в `waiting_answer` (dashboard → «Проверки и
  опросники»).
- Чатовые questionnaire HH: живьём подтверждён send-формат — text-кнопка
  отправляется обычным `POST /chatik/api/send` с точным текстом кнопки, а
  `send_event` остаётся для event-кнопок. Политика
  `profiles[].conversations.answer_known` отвечает на prompt reviewed
  conversation-блоком; задача идемпотентна по `(chat, prompt message, option)`,
  неизвестный или устаревший вариант остаётся человеку.
- Анкета вакансии HH подтверждена живьём: форма рендерится только при
  `startedWithQuestion=true` (иначе `vacancyTests` в состоянии отсутствует),
  live-страница использует `HH-Lux-InitialState`, а `required`/`multiple`/`open`
  приходят строками. Parser и capture исправлены, reference обновлён.
- Browser submit анкеты теперь заполняет choice-вопросы: radio и checkbox
  (`task_<id>=<solution-id>`), а вопрос с `open=true` и вариантами — веткой
  `task_<id>=open` с `task_<id>_text`. Ответ обязателен для каждого вопроса,
  code-вопросы по-прежнему не отправляются автоматически.
- Локальное удаление выбранных откликов через durable tasks; отдельный GC job
  с настраиваемым сроком и защитой приглашений.
- Сохранение переписки, квот, pacing, истории кампаний и dedup после очистки;
  миграции SQLite 19–20 с защитой от разрушительного downgrade.
- Поиск по всей базе, Unicode-фильтр компаний, категории, сортировка и страницы
  applications API; select→action в dashboard с результатом по каждому объекту.
- Отдельные широкие таблицы jobs/очереди, фильтры чатов, автоматическое чтение
  при открытии и блокировка повторного «Прочитать всё».
- Наблюдатель статусов HH через OAuth/API. Для browser-cookie профилей
  автоматический GC пока недоступен; подробности в `application-cleanup.md`.
- Временная подстройка резюме перед откликом: durable saga с baseline/target
  snapshot, apply и restore через profile state; включается
  `applications.tailoring.skills`, модель выбирает навыки с fallback на
  детерминированную политику; активная фаза, skill diff и recovery показываются
  в dashboard; подробности в `application-resume-tailoring.md`.
- Credential record в пакете `credentials`: чтение JSON и dotenv
  (`file:`/`dotenv-file:`), атомарная запись `0600` без symlink и случайной
  перезаписи; HH adapter читает refresh-совместимую запись.
- Auth control plane: durable auth session с revision CAS, эфемерный в памяти
  challenge store с TTL и HTTP endpoints для identifier/OTP/password/captcha,
  отмены и чтения redacted-состояния.
- HH search: `similar_resume`, `similar_vacancy` и `related_vacancy` через API и
  `similar_resume` через browser `/search/vacancy?resume=`; у source появилась
  матрица поддерживаемых полей.
- `resume.publish` через applicant API: job action, durable task, worker и
  нормализация ранней публикации в `rate_limited` с `next_publish_at`.
- Browser worker на Playwright с узким HTTP RPC: контексты профилей,
  storage state, page-примитивы, Bearer-токен и Compose-профиль `browser`.
- Интерактивный HH login через браузер: email → OTP/captcha, browser storage
  state как артефакт сессии (миграция 22) и CLI `job-agent auth login|status`.
- Извлечение вопросов vacancy test из popup initial state в
  `core.Questionnaire` (вопросы, варианты, open-text и code-задачи).
- Отправка open-text ответов vacancy test через popup-форму с проверкой
  `_xsrf`/`uidPk`/`guid`/`startTime` и обязательным read-back результата.
- Worker `test.capture`: наблюдаемые вопросы vacancy test попадают в
  прогрессивный тестовый каталог монотонным upsert, без запуска попытки.
- Worker `questionnaire.answer`: отправка уже resolved ответов через
  browser write transport (входные ответы не догадываются в worker-е).
- Worker `test.complete` и миграция 23: монотонный результат vacancy test по
  профилю, где `passed` не понижается поздним `failed`.
- Цепочка vacancy test: blocked application ставит идемпотентный
  `test.capture`; `submitted`/`passed` attempt снимает статический
  `has_test`-блок, и повторный submit продолжает pipeline.
- Known-answer routing: `answer_sets` в конфиге, vacancy-`AnswerBlock` и
  `UncoveredQuestions`; полное покрытие ставит `questionnaire.answer`,
  неполное создаёт детерминированную review session без submit, успешный
  submit фиксируется как `test.complete(submitted)`.
- Worker `review.answer` и миграция 24: runtime questionnaire хранится в
  review session; человеческий выбор записывается с CAS, при полном покрытии
  цепочка возобновляет `questionnaire.answer`, иначе выдаётся следующий
  missing prompt.
- Review API и CLI: `GET /api/v1/review-sessions/{id}` отдаёт текущий prompt,
  `POST .../answers` принимает выбор с Idempotency-Key и ставит durable
  `review.answer`; `job-agent review show|answer` работает как API-клиент.
- Append-only ревизии answer blocks (миграция 25): ответ человека сохраняется
  ревизией и переиспользуется в следующих capture через композит
  `ReviewedVacancyAnswers`; повтор идентичного ответа ревизию не создаёт.
- Bearer-токен API: `server.api_token_env` включает проверку заголовка
  `Authorization` (health-пробы открыты), не-loopback bind разрешён только с
  токеном; dashboard проксирует запросы со своим `JOB_AGENT_API_TOKEN`.
- Single-replica guard (миграция 26): startup берёт exclusive runtime lease в
  SQLite с renew и takeover после падения; второй живой инстанс завершается
  с понятной ошибкой.
- Config builder: top-level `include` с glob, depth-first merge, cycle/depth
  guard и ошибками с путём к файлу; файловые ссылки внутри include резолвятся
  относительно объявившего их файла.
- Наблюдаемость API: `X-Request-ID` (входящий сохраняется), структурный
  access-log через slog JSON и `/metrics` в Prometheus text (requests по
  классам, in-flight, uptime).
- Заморозка схемы конфига: `schema_version: 1`, неизвестная версия
  отклоняется.
- Администрирование БД: `job-agent db backup|restore` (VACUUM INTO, integrity
  и schema verification, `--force`, чистка stale WAL); `/metrics` показывает
  очередь задач, runtime-логи переведены на slog.
- LICENSE (MIT), `docs/quickstart.md` и `docs/runbook.md`; release checklist
  дополнен операционной проверкой backup/restore и upgrade/rollback.
- Resume aliases: `resume_aliases` резолвятся в `profile.resume` и job actions
  при загрузке конфига; `GET /api/v1/profiles/{profile}/resumes` отдаёт
  каталог targets (primary + aliases).
- Skill verification: attempt history и монотонный best по
  (platform, profile, family, level) в SQLite (миграция 27), включая
  unverified/failed-политику `PreferQualificationResult`.
- Skill verification catalog: offerings в SQLite (миграция 28), порты
  `QualificationCatalogReader`/`QualificationAttemptService`, worker
  `skill_verification.sync` и API
  `GET/POST /api/v1/profiles/{profile}/qualifications[/sync]`; discovery не
  запускает попытку.
- HH skill verification catalog: чтение
  `/applicant/skill_verifications/methods` через browser session, парсер
  уровней easy/medium/hard и theory/practice kinds; нераспознанная страница
  возвращается как `unsupported`, а не угадывается.
- Qualification runner: `skill_verification.start` отвечает из reviewed-блока
  (`ResolveQuestionAnswer` по fingerprint), неизвестный вопрос завершает
  попытку досрочно без случайного ответа, результат пишется в attempt
  history/best, пройденный уровень не перезапускается.
- Qualification review loop: review session несёт `answer_block_tag` и
  человеческие ответы дописываются ревизией в qualification-блок family/level;
  `POST /api/v1/profiles/{profile}/qualifications/{offering}/start` ставит
  durable попытку с Idempotency-Key.
- CLI `job-agent qualification catalog|sync|start` как API-клиент поверх
  qualification endpoints.
- Auth SSE: `GET /api/v1/auth/sessions/{id}/events` стримит ревизии сессии и
  завершается на terminal-статусе (completed/expired/cancelled/failed).
- Dashboard auth wizard: секция «Вход в HH» (создание сессии, SSE-подписка,
  captcha, отправка identifier/OTP/password/captcha и отмена).
- Auth restart recovery: незавершённые сессии при старте backend помечаются
  `failed` с категорией `temporary_failure` и понятным сообщением.
- CLI `job-agent auth status --watch` следует за SSE-потоком сессии.
- Resume update: durable `resume.update` (payload, idempotency key, workflow),
  worker plan→apply→read-back→publish, API
  `POST /api/v1/profiles/{profile}/resumes/{resume}/update` и main-проводка;
  no-change план пропускает apply, но явно запрошенный publish повторяется.
- `auth logout`: `POST /api/v1/profiles/{profile}/logout` удаляет локальный
  credential record и browser state профиля (идемпотентно, с CLI-командой);
  платформенные токены не отзываются.
- Терминальные рендеры auth-challenge: Kitty graphics, Sixel с web-палитрой,
  Unicode half-block preview и приватный PNG-файл как fallback.
- Плановый `resume.update`: job action с `resource` и optional `publish`,
  cron-триггер и те же durable plan/apply/read-back/publish semantics без
  подтверждения; при platform cooldown задача ждёт `Retry-After`, а scheduler
  не создаёт дубликат активного запуска. Worker `resume.update` регистрируется
  при наличии writer, без publisher допускается только apply.
- История ревизий profile state (миграция 29): каждый подтверждённый apply
  записывает redacted revision (before/after digests, изменённые paths,
  `source`, `applied_at`); API `GET /api/v1/profile-state/revisions` с
  фильтрами и панель «История ревизий» в dashboard.
- Dashboard review: `GET /api/v1/review-sessions` с фильтрами по
  статусу/профилю/платформе и текущим вопросом; секция «Проверки и опросники»
  рендерит single/multiple/text prompt и записывает ответ через durable
  `review.answer` с Idempotency-Key.
- Dashboard-редактор profile state: редактор показывает объявленные
  resume-scoped leaves со значением text/`null`, а не только `about`;
  массивы, объекты, числа и boolean остаются вне формы, writer
  по-прежнему проверяет allowlist и подтверждает read-back.
- Контракт model fallback для неизвестных вопросов тестов и опросников:
  цепочка known-answer → model (validator) → manual, границы
  contextual/code, provenance и режимы `auto_submit`/`review_only`
  (`docs/next-answer-model-fallback.md`); реализация не начата.
- Model fallback для qualification в режиме auto: профиль может задать
  `answers.model` (provider/prompt_version/instruction/timeout). Ответ модели
  проходит локальный validator по показанным вариантам, `code` и ошибки модели
  уходят в обычный review, успешный `QualificationResult` дописывает verified
  ответ с provenance в qualification-блок; без declarative-блока используется
  conventional reviewed tag.
- HH skill verification catalog: парсер читает `HH-Lux-InitialState`
  (`skillsVerificationMethodsPage.items`) с family ID, уровнями
  (`internalId`/`rank`) и доступностью видов (`theory`/`practice`); legacy
  HTML-разметка остаётся fallback. Живой E2E подтвердил, что карточки больше
  не содержат ссылок `/applicant/skills/<id>/verification_methods`.
- Fix: SQLite сохраняет `review_sessions.answer_block_tag` (миграция 30).
  Колонки не было, поэтому review-сессия квалификации читалась без целевого
  блока и worker не мог дополнить family/level ревизию.
- Browser worker: `BROWSER_WORKER_EXECUTABLE` позволяет запускать worker на
  системном браузере, когда Playwright не находит его в стандартных путях;
  `BROWSER_WORKER_CHANNEL=chrome` для этого недостаточно.
- Ошибки HH: `403` на конкретную вакансию или browser-страницу больше не
  считается истёкшей сессией, а переводится в `permanent_failure`; `401`
  по-прежнему означает сброс авторизации. Недоступная аккаунту вакансия
  больше не зацикливает retry.
- Операторский retry заблокированного или упавшего отклика:
  `POST /api/v1/applications/{id}/retry` (Idempotency-Key) очищает decision и
  записанную ошибку, возвращает отклик в `ready` и ставит новый durable
  `application.submit`; повтор ключа идемпотентен, dashboard показывает кнопку
  «Повторить» для `waiting_validation` и `failed`.
- Наблюдаемость задач: worker логирует `task retry scheduled` и
  `task failed` с type/category/error, поэтому `handler failed` больше не
  скрывает причину в логах. Исчерпанные revision-конфликты conversation
  discovery классифицируются как `temporary_failure` и ретраятся.
- Runbook: восстановление после смены resume hash (activity observe,
  resume.touch, resume_not_suitable).
- Live assessment контракт skill verification: зафиксированы формы
  `get_current_task`/`submit_user_answer`/`get_contest_tasks`, экран результата
  и месячный lock после использованной попытки.
- Conversation discovery на живом аккаунте с 3474 чатами больше не падает:
  адаптер читает recent-окно (1000 последних по активности) и отдаёт
  `Truncated`, а sync планируется только для новых/изменённых диалогов
  (статус, unread или новое последнее сообщение). Обновление каталога и
  presentation ретраится на optimistic revision conflict, поэтому параллельные
  sync-задачи не валят discovery.
- Порядок HTTP-обвязки: `RequestID` теперь снаружи access-log, поэтому
  `request_id` всегда заполнен; Bearer-токен защищает `/metrics` и
  auth-endpoints, access-log покрывает auth-запросы. Запросы, отклонённые
  Bearer, остаются в access-log, но не попадают в счётчики статусов.
- `make smoke` и `scripts/smoke-runtime.sh`: сборка, миграция пустой БД,
  запуск backend, проверка health/ready/endpoints/request-id и round-trip
  `db backup`/`db restore` без сети и без рабочего `data/`.

Это development-версия, не production-релиз `v1`. Миграции и GC на рабочей базе
в рамках разработки не запускались; режим отправки откликов не менялся.
