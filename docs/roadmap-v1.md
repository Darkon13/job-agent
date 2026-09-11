# Roadmap до v1.0.0

Статус: active. Решения о границе релиза зафиксированы 2026-09-12.

Цель v1.0.0 — личный, но надёжный инструмент для ежедневной работы с HH:
интерактивный вход, поиск, кампании откликов, сопроводительные, живое
прохождение тестов, управление резюме, чаты и локальная очистка — в одном
долгоживущем сервисе с dashboard. Публичный multi-user продукт, Telegram,
календарь и MCP сознательно вынесены за v1.0.0.

## Принятые решения

1. **Граница релиза:** личный v1.0 (один владелец, SQLite, один backend,
   loopback/WG-доступ). Публичный продукт и PostgreSQL — после v1.
2. **Browser worker:** отдельный TypeScript-сервис на официальном Playwright с
   узким HTTP JSON RPC; один Chromium, persistent context на профиль, bounded
   page pool, per-profile mutation lock. Go остаётся владельцем бизнес-логики.
3. **Порядок работ:** auth → live tests → resume lifecycle → hardening.
4. **Интеграции:** Telegram, календарь, MCP и полноценный web UI — после v1.
5. **API auth:** минимальный Bearer-токен из secret volume для не-loopback
   эксплуатации.
6. **Идентичность:** module path `github.com/Darkon13/job-agent`, имя проекта и
   SQLite + DB-backed queue сохраняются.

## M0. Контракт релиза

- [x] Зафиксировать решения и границу v1.0.0 (этот документ).
- [x] Обновить критерии готовности в `docs/releasing.md`.
- [x] Починить устаревшие факты в `docs/project-status.md` (схема 23, publish).
- [ ] Зафиксировать открытые архитектурные решения: RPC формат (HTTP JSON),
      storage, queue, config includes (см. M5).

## M1. Browser worker и RPC

- [x] `docs/browser-rpc.md`: операции, DTO, ошибки, таймауты, security,
      per-profile context, bounded page pool, VNC fallback.
- [x] Go-пакет `browser/`: типизированный клиент, per-profile lock, маппинг
      ошибок в core-категории, проверка протокола.
- [x] `browser/browsertest`: in-memory fake для контрактных тестов.
- [x] TS-сервис `browser-worker/`: Playwright, контексты профилей, health и
      readiness, Bearer-токен, graceful shutdown.
- [x] Compose-сервис `browser-worker` (профиль `browser`) + volume contexts.
- [x] Операции v1: `health`, `ensure`, `storage_state`, `goto`, `screenshot`,
      `click`, `fill`, `press`, `wait`, `content`, `page`, `close`.
- [x] Unit-тесты worker: config и HTTP-поверхность (валидация, Bearer-токен,
      лимиты тела).
- [ ] DoD: VNC E2E smoke; подключение клиента к backend (M2) с
      `BROWSER_WORKER_URL`/`BROWSER_WORKER_TOKEN`.

## M2. Auth control plane

- [x] HH login driver через browser RPC: email → OTP/captcha; password branch
      фиксируется как `unsupported`, deadline шага ограничен.
- [x] Browser storage state как второй артефакт сессии (`BrowserStateWriter`,
      миграция 22) и сохранение после входа.
- [x] CLI `job-agent auth login|status` с TTY-prompts, presenter-рендерами и
      проверкой API version.
- [x] `auth import` санитизирует существующий storage state; `logout` и
      `--presenter dashboard|vnc` остаются.
- [ ] SSE переходов сессии и dashboard auth wizard.
- [ ] Refresh/revoke, `auth_required`, restart recovery.
- [ ] DoD: 9 критериев из `docs/next-auth-control-plane.md`, VNC E2E login.

## M3. Live vacancy tests и опросники

- [x] Capture vacancy test: popup initial state → `core.Questionnaire`
      (browser read channel, без хранения xsrf/guid).
- [x] Submit ответов и read-back результата (open-text tasks; choice/code
      отклоняются как unsupported до live-проверки их полей).
- [ ] Skill verification easy/medium/hard: sync каталога, прогон с capture
      вариантов ответа, выбор правильных (known/model/human review), запись
      reusable answer block per family/level и автоматическое прохождение
      последующих попыток через job-agent.
- [x] Worker `test.capture`: browser capture → прогрессивный тестовый
      каталог (монотонный upsert, без попытки и submit).
- [x] Worker `questionnaire.answer`: submit заранее resolved ответов через
      write transport.
- [x] Worker `test.complete`: монотонный результат попытки по профилю
      (миграция 23).
- [x] Producer и gate (решение: автоматически, review-фолбэк): blocked
      application → `test.capture`; запись `submitted`/`passed` attempt снимает
      статический `has_test`-блок и возвращает pipeline к submit.
- [x] Полное покрытие vacancy-`AnswerBlock` → `questionnaire.answer`;
      неполное → review session без submit; после успешного submit →
      `test.complete`(`submitted`); contextual-вопросы без ответа уходят в
      manual review.
- [x] Worker `review.answer`: CAS-запись человеческого выбора; при полном
      покрытии возобновляет `questionnaire.answer`, иначе выдаёт следующий
      missing prompt (runtime questionnaire хранится в review session,
      миграция 24).
- [x] Review API + CLI: `GET /review-sessions/{id}` с текущим prompt и
      `POST /review-sessions/{id}/answers` с Idempotency-Key.
- [x] Append-only ревизии answer blocks: `answer_block_revisions` (миграция
      25); человеческие ответы пишутся ревизией и подмешиваются композитом
      `ReviewedVacancyAnswers` (latest revision wins), повтор без изменений
      ревизию не создаёт.
- [ ] Dashboard review.
- [ ] Опросники из чатов через тот же registry.
- [ ] DoD: реальный тест пройден, повтор по сохранённым ответам, evidence
      записан.

## M4. Жизненный цикл резюме

- [ ] `resume.create`/`resume.update` как desired state с deterministic/model
      processor, semantic diff и approval policy.
- [ ] Publish после update и read-back; `bootstrap.when: missing_resume`.
- [x] Алиасы (`resume_aliases`, резолв в `profile.resume` и job actions) и
      каталог targets (`GET /api/v1/profiles/{profile}/resumes`).
- [ ] Dashboard-редактор резюме и история ревизий.
- [ ] DoD: создание резюме из bootstrap-файла и безопасный update с publish.

## M5. Hardening для v1.0.0

- [x] Bearer-токен для API (`server.api_token_env`) и dashboard
      (`JOB_AGENT_API_TOKEN`, внедряется proxy server-side); health-пробы
      остаются открытыми, loopback-контракт сохранён без токена.
- [x] Явный single-replica guard: exclusive runtime lease в SQLite (TTL 90s,
      renew, takeover после падения); второй живой инстанс останавливается.
      Shared mutation lease для нескольких реплик — вне v1.
- [x] Config builder: `include` с рекурсией, glob, cycle/depth guard; ошибки
      несут путь к файлу; ссылки внутри include-файла резолвятся от него.
- [x] Заморозка схемы конфига: `schema_version` (текущая 1) в главном файле;
      неизвестная версия отклоняется.
- [x] Request ID (`X-Request-ID`), структурный access-log (slog JSON) и
      `/metrics` (Prometheus text: requests по классам, in-flight, uptime,
      очередь задач по type/status/priority); runtime-логи идут через slog.
- [x] Backup/restore: `job-agent db backup|restore` (VACUUM INTO, integrity и
      schema verification, `--force` destructive guard, чистка stale WAL);
      upgrade/rollback миграций покрыты тестами и runbook-ом.
- [x] CI (`make verify` c `go test -race`, browser-worker), LICENSE, release
      checklist с операционной проверкой в `docs/releasing.md`.
- [x] Quickstart для чистого аккаунта и runbook восстановления
      (`docs/quickstart.md`, `docs/runbook.md`): auth_required,
      recovery_required, failed apply, quota, runtime lease, token, worker.
- [ ] DoD: чистый install с нуля на пустом `data/`, `make release-check`,
      выпуск `v1.0.0` по `docs/releasing.md` (нужен реальный аккаунт).

## M6. После v1.0.0 (не блокирует релиз)

Telegram-уведомления и роль рекрутера, календарь, MCP endpoint, полноценный
web UI, PostgreSQL/Redis, внешние Go-модули адаптеров.
