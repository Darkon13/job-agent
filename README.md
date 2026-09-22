# Job Agent

Локальный сервис автоматизации поиска работы: ищет вакансии на подключённых
платформах, фильтрует и распределяет их между профилями, готовит
сопроводительные, отправляет отклики, заполняет анкеты и тесты на странице
вакансии, отвечает на опросники в чатах и ведёт переписку с рекрутерами.

Всё работает на вашей машине: конфигурация, база, резюме и browser-сессии
остаются локальными и не отправляются третьим лицам.

**Документация: <https://darkon13.github.io/job-agent/>** — quickstart,
справочник конфигурации, шпаргалки и живые контракты платформ.

- **Декларативная конфигурация** (подход вдохновлён Xray и sing-box): профили,
  поиски, jobs и правила описываются небольшими JSON-файлами и валидируются
  до запуска.
- **Адаптеры платформ**: core не знает о конкретной площадке. HeadHunter идёт
  первым; новые платформы добавляются отдельными пакетами —
  см. [`docs/writing-adapters.md`](docs/writing-adapters.md).
- **API-first с browser fallback**: где у платформы есть API — используется он,
  где нет (анкеты, тесты, чаты, поднятие резюме) — Playwright-worker.
- **Безопасные режимы**: `dry_run` (платформа не меняется), `approval` и
  `submit` с дневными лимитами, pacing и бюджетами.
- **Durable-задачи**: каждая внешняя операция переживает рестарт, повтор
  идемпотентен, неоднозначные исходы сверяются.
- **Dashboard** на localhost: профили, отклики, чаты, проверки и опросники,
  jobs, история ревизий резюме.

> Job Agent — инструмент для вашего личного аккаунта. Используйте его в рамках
> правил платформ; он не предназначен для обхода защитных механизмов.

## Быстрый старт

Нужен только Docker с Compose v2 — либо Go 1.26 и Node 22, если запускаете из
исходников. Подробный маршрут с пустого аккаунта — в
[`docs/quickstart.md`](docs/quickstart.md); ниже этапы от установки до первых
реальных откликов.

### Этап 1. Получите дистрибутив

- **Архив релиза** со страницы Releases (`job-agent_<версия>_linux_amd64.tar.gz`):
  внутри фасад `job-agent` (сервер и все утилиты подкомандами),
  `job-agent-migrate`, `job-agent-dashboard`, `compose.yaml`, `.env.example`
  и примеры `deploy/`.
- **Готовые образы**: `ghcr.io/darkon13/job-agent:<версия>` и
  `ghcr.io/darkon13/job-agent-browser-worker:<версия>` (slim, системный
  Chromium).
- **Из исходников**: `git clone` и `make build`.

### Этап 2. Настройте окружение

```sh
scripts/init-env.sh                  # .env с сгенерированными токенами
cp deploy/config.example.json deploy/config.json
```

Скрипт создаёт `.env` из `.env.example` и подставляет случайные
`BROWSER_WORKER_TOKEN` и `JOB_AGENT_API_TOKEN` — их backend и browser-worker
используют как общий секрет, придумывать вручную ничего не нужно. В git-чекауте
доступен и ярлык `make init`. Шаг выполняется один раз вручную: Compose не
умеет генерировать секреты, поэтому при пропущенном токене browser-worker
падает с подсказкой запустить этот скрипт.

В `deploy/config.json` замените `replace-with-hh-resume-id` на ID резюме HH
(виден в ссылке на резюме в кабинете). Имя профиля `main` — произвольный тег:
назовите профиль как удобно и используйте это имя в ссылках. Поиск с fallback,
поднятие резюме и рассылка откликов уже описаны и выполняются по расписанию.

### Этап 3. Запустите сервисы

```sh
mkdir -p data
docker compose --profile browser up -d
```

Compose сам применяет миграции (`-migrate-up`) и поднимает backend, dashboard
и browser-worker.

<details>
<summary>Вариант без Docker: локальные бинарники</summary>

```sh
make build
export PATH="$PWD/dist:$PATH"
export JOB_AGENT_API_TOKEN="$(openssl rand -hex 32)"
job-agent-migrate -config deploy/config.json up
job-agent deploy/config.json          # backend, API на 127.0.0.1:8080
job-agent-dashboard                   # dashboard на 127.0.0.1:8081
```

Для browser-only операций (анкеты, тесты, чаты, вход в HH) дополнительно
запустите browser-worker — см. [`browser-worker/README.md`](browser-worker/README.md).
</details>

### Этап 4. Проверьте конфигурацию и API

```sh
docker compose run --rm job-agent /usr/local/bin/job-agent \
  check /config/config.json
curl -sf http://127.0.0.1:8081/dashboard-healthz
```

`job-agent check` печатает `OK config`, наличие ключей моделей, схему БД и
очередь задач — та же диагностика, что раньше делал `job-agent-check`.
Backend наружу не публикуется: dashboard проксирует его API.

### Этап 5. Войдите в HH

Откройте dashboard `http://127.0.0.1:8081` → «Вход в HH» → профиль `main` →
«Начать вход». Сессия сохранится в browser-профиль и переживёт перезапуск.
При запуске локальными бинарниками доступен и CLI:

```sh
job-agent auth login --profile main --state-output ./data/profiles/main.json
```

### Этап 6. Прогоните dry-run

```sh
docker compose run --rm job-agent /usr/local/bin/job-agent \
  trigger -idempotency-key "$(date +%s)" /config/config.json daily-applications
```

То же самое делает кнопка «Выполнить» в разделе «Автоматизация из
конфигурации» dashboard. В `dry_run` сервис находит вакансии и готовит письма,
но платформу не меняет: проверьте тексты в `deploy/messages/backend.json` и
фильтры `qualification` профиля.

### Этап 7. Включите реальные отклики

В `deploy/config.json` у профиля:

```json
"applications": {
  "mode": "submit",
  "daily_limit": 30,
  "submit_jitter": {"min": "15s", "max": "30s"}
}
```

Перезапустите сервис (`docker compose up -d`). `daily_limit` задаёт дневной
потолок откликов, `submit_jitter` — паузу между отправками; лимиты платформы
соблюдаются отдельно. Если вакансия требует анкету, отклик остановится в
разделе «Проверки и опросники» — ответьте там, и отправка продолжится сама.

### Этап 8. Эксплуатация

```sh
docker compose run --rm job-agent /usr/local/bin/job-agent \
  db backup -config /config/config.json          # согласованный снимок БД
job-agent review show --session <id> --api http://127.0.0.1:8081
job-agent qualification catalog --profile main --api http://127.0.0.1:8081
job-agent approve -idempotency-key K deploy/config.json main 12345678
```

Остальные подкоманды фасада: `browser-state sanitize`, `question-bank-import`,
`startup`, `profile bootstrap`. Backup/restore, типовые сбои и восстановление
описаны в [`docs/runbook.md`](docs/runbook.md).

## Как это работает

```text
config -> router -> workflows -> adapter facade
                                  |- platform API
                                  `- browser worker

events -> broker -> deterministic / LLM / MCP / human operators
                                             |
                                      durable tasks
```

- `core` — доменные типы, задачи, ошибки, questionnaire и review;
- `adapter`/`adapters/*` — контракты и реализации платформ;
- `workflow` — прикладные сценарии (рассылки, отклики, чаты, резюме);
- `worker` — обработчики durable-задач;
- `storage` — SQLite и in-memory repositories;
- `browser-worker` — опциональный Playwright RPC для browser-only операций.

Подробности архитектуры, режимов и контрактов — в
[`docs/features.md`](docs/features.md) и [`docs/index.md`](docs/index.md).

## Документация

| Документ | О чём |
|---|---|
| <https://darkon13.github.io/job-agent/> | вики: поиск по документации, навигация, справочник и рецепты |
| [`docs/quickstart.md`](docs/quickstart.md) | подробный маршрут с пустого аккаунта |
| [`docs/runbook.md`](docs/runbook.md) | эксплуатация, backup/restore, восстановление |
| [`docs/features.md`](docs/features.md) | подробный обзор возможностей и runtime |
| [`docs/writing-adapters.md`](docs/writing-adapters.md) | как добавить платформу |
| [`docs/hh-*.md`](docs/) | живые контракты HeadHunter |
| [`docs/roadmap-v1.md`](docs/roadmap-v1.md) | план и статус выпуска |
| [`docs/changelog.md`](docs/changelog.md) | история изменений |

## Разработка

```sh
make verify     # go test -race ./..., go vet, git diff --check
make smoke      # runtime smoke на пустой БД
```

Для browser-worker: `cd browser-worker && npm ci && npm run typecheck && npm test`.
Правила и процесс PR — в [`CONTRIBUTING.md`](CONTRIBUTING.md). CI на GitHub
запускает всё это на каждый push и pull request.

## Лицензия

См. [`LICENSE`](LICENSE).
