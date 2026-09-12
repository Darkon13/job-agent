# Изменения

## 0.3.0-dev

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

Это development-версия, не production-релиз `v1`. Миграции и GC на рабочей базе
в рамках разработки не запускались; режим отправки откликов не менялся.
