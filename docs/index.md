# Job Agent

Локальный сервис автоматизации поиска работы: ищет вакансии, готовит и
отправляет отклики, проходит анкеты и тесты, ведёт переписку с рекрутерами и
показывает всё это в локальном dashboard. Платформы подключаются адаптерами —
core не знает о конкретной площадке.

Сервис управляется декларативным конфигом: опишите адаптеры, профили, поиски
и jobs, и он сам выполнит поиск, поднятие резюме и отклики по расписанию.

**Новичкам:** начните с [Quickstart](quickstart.md) — от копии конфига до
первых dry-run откликов.

Job Agent — локальный сервис автоматизации поиска работы. Он ищет вакансии,
готовит и отправляет отклики, проходит анкеты и тесты, ведёт переписку с
рекрутерами и показывает всё это в локальном dashboard. Платформы подключаются
адаптерами: core не знает о конкретной площадке.

Если вы здесь впервые — начните с [`quickstart.md`](quickstart.md). Этот файл
нужен, чтобы понять, какой документ читать дальше.

## С чего начать

| Документ | Когда открывать |
|---|---|
| [`quickstart.md`](quickstart.md) | первый запуск с пустого аккаунта: конфигурация, вход, dry-run, реальные отклики |
| [`features.md`](features.md) | подробный обзор возможностей и runtime-деталей: режимы, адаптеры, operators, чаты, dashboard |
| [`runbook.md`](runbook.md) | эксплуатация: backup/restore, миграции, типовые сбои и восстановление |

## Конфигурация и состояние профиля

| Документ | О чём |
|---|---|
| [`profile-desired-state-and-routing.md`](profile-desired-state-and-routing.md) | desired state профиля: plan → apply → read-back, named actions, rule sets |
| [`profile-bootstrap.md`](profile-bootstrap.md) | первичное заполнение профиля из декларативного манифеста |
| [`application-resume-tailoring.md`](application-resume-tailoring.md) | временная подстройка резюме под вакансию и безопасный откат |
| [`application-cleanup.md`](application-cleanup.md) | retention/GC откликов с tombstone и повторной проверкой |

## API и интеграции

| Документ | О чём |
|---|---|
| [`conversation-api.md`](conversation-api.md) | control plane чатов: список, sync, mark-read, отправка, follow-up |
| [`browser-rpc.md`](browser-rpc.md) | контракт browser worker: операции, DTO, ошибки, security |
| [`dashboard-runtime.md`](dashboard-runtime.md) | устройство dashboard, reverse proxy и очередей |

## Контракты HeadHunter

Живые контракты, зафиксированные по реальным запросам. Начинать с
[`hh-api-contracts.md`](hh-api-contracts.md).

| Документ | О чём |
|---|---|
| [`hh-api-contracts.md`](hh-api-contracts.md) | API-контракты HH и нормализация ошибок |
| [`hh-auth-flow.md`](hh-auth-flow.md) | OAuth и browser-вход, хранение сессий |
| [`hh-browser-search.md`](hh-browser-search.md) | поиск и пагинация через web |
| [`hh-browser-operations.md`](hh-browser-operations.md) | отклик, анкеты, тесты, activity, поднятие резюме |
| [`hh-chat-contract.md`](hh-chat-contract.md) | чаты: chat_data, отправка, кнопки-опросники |
| [`hh-resume-contract.md`](hh-resume-contract.md) | создание, редактирование и публикация резюме |
| [`hh-applicant-tool-reference.md`](hh-applicant-tool-reference.md) | инвентарь поведения исходного инструмента-референса |

## Развитие проекта

| Документ | О чём |
|---|---|
| [`writing-adapters.md`](writing-adapters.md) | как добавить новую платформу: границы, capability, ошибки, тесты |
| [`roadmap-v1.md`](roadmap-v1.md) | план выпуска и текущий статус |
| [`releasing.md`](releasing.md) | версии, release-check, известные ограничения |
| [`changelog.md`](changelog.md) | история выпущенных изменений |
| [`next-answer-model-fallback.md`](next-answer-model-fallback.md) | цепочка ответов known-answer → model → review |
| [`next-auth-control-plane.md`](next-auth-control-plane.md) | единый auth flow профилей и presenter-клиенты |
| [`next-cover-letter-routing.md`](next-cover-letter-routing.md) | routing сопроводительных: пулы, model, fallback |
