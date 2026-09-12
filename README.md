# Job Agent

Локальный сервис автоматизации поиска работы: ищет вакансии на подключённых
платформах, фильтрует и распределяет их между профилями, готовит
сопроводительные, отправляет отклики, заполняет анкеты и тесты на странице
вакансии, отвечает на опросники в чатах и ведёт переписку с рекрутерами.

Всё работает на вашей машине: конфигурация, база, резюме и browser-сессии
остаются локальными и не отправляются третьим лицам.

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

Понадобится Docker с Compose v2 — или Go и Node 22, если запускаете из
исходников. Полный пошаговый маршрут с нуля — в
[`docs/quickstart.md`](docs/quickstart.md).

### 1. Конфигурация

```sh
git clone https://github.com/Darkon13/job-agent.git
cd job-agent
cp deploy/config.example.json deploy/config.json
```

Откройте `deploy/config.json` и замените `replace-with-hh-resume-id` на ID
своего резюме HH (виден в ссылке на резюме в кабинете). Имя профиля `main` —
произвольный тег: назовите профиль как удобно и используйте это имя в ссылках.
В конфиге уже есть поиск с fallback, ежедневное поднятие резюме и рассылка
откликов — сервис выполнит их сам по расписанию.

### 2. Запуск (Docker Compose)

```sh
mkdir -p data
export JOB_AGENT_API_TOKEN="$(openssl rand -hex 32)"
JOB_AGENT_CONFIG_DIR=./deploy JOB_AGENT_DATA_DIR=./data \
  docker compose --profile browser up -d --build
```

Проверка:

```sh
curl -sf -H "Authorization: Bearer $JOB_AGENT_API_TOKEN" \
  http://127.0.0.1:8080/api/v1/version
curl -sf http://127.0.0.1:8081/dashboard-healthz
```

<details>
<summary>Альтернатива: локальные бинарники без Docker</summary>

```sh
make build
export JOB_AGENT_API_TOKEN="$(openssl rand -hex 32)"
./dist/job-agent-migrate -config deploy/config.json up
./dist/job-agent deploy/config.json          # backend, API на 127.0.0.1:8080
./dist/job-agent-dashboard                   # dashboard на 127.0.0.1:8081
```

Для входа в HH из исходников дополнительно запустите browser-worker
(см. [`browser-worker/README.md`](browser-worker/README.md)).
</details>

### 3. Один раз войдите в HH

Вход и dashboard — опциональные удобства: сам сервис работает по конфигу и
cron. Сохраните browser-сессию профиля любым способом:

```sh
job-agent auth login --profile main --state-output ./data/profiles/main.json
# или импорт готового state:
job-agent auth import --source export.json \
  --state-output ./data/profiles/main.json --force
```

Либо откройте dashboard `http://127.0.0.1:8081` → «Вход в HH» → профиль
`main` → «Начать вход». Сессия переживёт перезапуск, а дальше сервис работает
без вашего участия.

### 4. Проверьте dry-run

Запустите рассылку вручную из dashboard (раздел «Задания» → `Запустить`) или
через API:

```sh
curl -sf -X POST -H "Authorization: Bearer $JOB_AGENT_API_TOKEN" \
  -H "Idempotency-Key: $(uuidgen)" \
  http://127.0.0.1:8080/api/v1/jobs/daily-backend-applications/runs
```

В `dry_run` сервис делает всё, кроме отправки: находит вакансии, готовит
письма и показывает, какие отклики были бы отправлены. Проверьте тексты в
`deploy/messages/backend.json` и фильтры `qualification` в профиле — платформа
не меняется. Несколько HH-аккаунтов описываются отдельными профилями: пример и
правила в [`docs/quickstart.md`](docs/quickstart.md).

### 5. Включите реальные отклики

В `deploy/config.json` у профиля:

```json
"applications": {
  "mode": "submit",
  "daily_limit": 30,
  "submit_jitter": {"min": "15s", "max": "30s"}
}
```

Перезапустите сервис. `daily_limit` задаёт дневной потолок откликов, а
`submit_jitter` — паузу между отправками; сервис соблюдает и их, и лимиты
платформы. Если вакансия требует анкету, отклик остановится в разделе
«Проверки и опросники» — ответьте там, и отправка продолжится сама.

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
