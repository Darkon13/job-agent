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
- [ ] Adapter qualification service: sync каталога, capture, submit.
- [x] Worker `test.capture`: browser capture → прогрессивный тестовый
      каталог (монотонный upsert, без попытки и submit).
- [x] Worker `questionnaire.answer`: submit заранее resolved ответов через
      write transport.
- [x] Worker `test.complete`: монотонный результат попытки по профилю
      (миграция 23).
- [ ] Worker `review.answer`.
- [ ] Цепочка ответа: known-answer → model → human; ревизии answer blocks;
      contextual-вопросы только в manual.
- [ ] Опросники из чатов через тот же registry.
- [ ] Review UI в dashboard; CLI review.
- [ ] DoD: реальный тест пройден, повтор по сохранённым ответам, evidence
      записан.

## M4. Жизненный цикл резюме

- [ ] `resume.create`/`resume.update` как desired state с deterministic/model
      processor, semantic diff и approval policy.
- [ ] Publish после update и read-back; `bootstrap.when: missing_resume`.
- [ ] Алиасы и каталог resume targets.
- [ ] Dashboard-редактор резюме и история ревизий.
- [ ] DoD: создание резюме из bootstrap-файла и безопасный update с publish.

## M5. Hardening для v1.0.0

- [ ] Bearer-токен для API и dashboard; обновить deployment-документацию.
- [ ] Shared mutation lease в SQLite либо явный single-replica guard.
- [ ] Config builder: include/glob и точные ошибки с путём к файлу; заморозка
      схемы конфига.
- [ ] Метрики, структурные логи, request/correlation ID.
- [ ] Backup/restore и проверка upgrade/rollback, включая destructive guard.
- [ ] CI (`make verify`, `go test -race`), LICENSE, release checklist.
- [ ] Quickstart для чистого аккаунта (browser login) и runbook'и
      восстановления: `auth_required`, `recovery_required`, failed apply, quota.
- [ ] DoD: чистый install с нуля на пустом `data/`, `make release-check`,
      выпуск `v1.0.0` по `docs/releasing.md`.

## M6. После v1.0.0 (не блокирует релиз)

Telegram-уведомления и роль рекрутера, календарь, MCP endpoint, полноценный
web UI, PostgreSQL/Redis, внешние Go-модули адаптеров.
