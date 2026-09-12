# Quickstart: с нуля до первых откликов

Маршрут поднимает Job Agent с пустого каталога `data/`: конфигурация, вход в
HH, пробная кампания в `dry_run` и переход к реальным откликам. Варианты
запуска — Docker Compose (проще) или локальные бинарники (для разработки).

Самый быстрый путь — взять готовый пример и поменять в нём резюме:

```sh
git clone https://github.com/Darkon13/job-agent.git
cd job-agent
cp -r config/example deploy
mkdir -p data
```

Дальше достаточно заменить `replace-with-hh-resume-id` в `deploy/config.json`
на ID своего резюме HH, экспортировать токен API и запустить:

```sh
export JOB_AGENT_API_TOKEN="$(openssl rand -hex 32)"
JOB_AGENT_CONFIG_DIR=./deploy JOB_AGENT_DATA_DIR=./data \
  docker compose --profile browser up -d --build
```

Если хочется понять, из чего собран пример, — ниже разобран эквивалент с нуля.

## 1. Конфигурация

```text
job-agent/
|- deploy/
|  |- config.json
|  |- profiles.json
|  |- messages/backend.json
|  `- searches/golang.json
`- data/
```

`deploy/config.json` — единственный файл с `database`, `server` и `include`
(остальные объекты можно держать в include-файлах):

```json
{
  "schema_version": 1,
  "database": {"driver": "sqlite", "path": "./data/job-agent.db"},
  "server": {
    "listen": "127.0.0.1:8080",
    "api_token_env": "JOB_AGENT_API_TOKEN"
  },
  "include": ["profiles.json", "searches/*.json"]
}
```

`deploy/profiles.json` описывает адаптер и профиль:

```json
{
  "adapters": [{"tag": "hh-main", "type": "hh"}],
  "profiles": [
    {
      "tag": "primary",
      "adapter": "hh-main",
      "resume": "replace-with-hh-resume-id",
      "state_file": "./data/profiles/primary.json",
      "enabled": true,
      "applications": {
        "mode": "dry_run",
        "message_template_file": "messages/backend.json",
        "qualification": {"include_any": ["Go", "Golang", "Backend"]},
        "timezone": "Europe/Moscow"
      },
      "conversations": {"allow_send": false, "allow_mark_read": false}
    }
  ]
}
```

`deploy/messages/backend.json` — пул сопроводительных:

```json
{
  "strategy": "stable_hash",
  "templates": [
    {
      "tag": "concise",
      "template": "Здравствуйте! Заинтересовала вакансия {{.Vacancy.Title}} в {{.Vacancy.Employer}}. Буду рад обсудить задачи команды."
    }
  ]
}
```

`deploy/searches/golang.json` добавляет поиск:

```json
{
  "searches": [
    {
      "tag": "golang",
      "adapter": "hh-main",
      "profiles": ["primary"],
      "query": {"source": "global", "text": "Golang developer", "area": ["1"], "page_size": 20}
    }
  ]
}
```

Начните с `"mode": "dry_run"`: письма и решения готовятся, платформа не
меняется. Реальный submit включается после проверки письма, лимитов и
pacing-политики.

## 2. Запуск

### Docker Compose

```sh
export JOB_AGENT_API_TOKEN="$(openssl rand -hex 32)"
JOB_AGENT_CONFIG_DIR=./deploy JOB_AGENT_DATA_DIR=./data \
  docker compose --profile browser up -d --build
```

Профиль `browser` добавляет browser worker, который нужен для входа в HH и
browser-only операций (анкеты, тесты, чаты). Проверка:

```sh
curl -sf -H "Authorization: Bearer $JOB_AGENT_API_TOKEN" \
  http://127.0.0.1:8080/api/v1/version
curl -sf http://127.0.0.1:8081/dashboard-healthz
```

### Локальные бинарники

```sh
make build
export JOB_AGENT_API_TOKEN="$(openssl rand -hex 32)"
./dist/job-agent-migrate -config ./deploy/config.json up
./dist/job-agent ./deploy/config.json
```

Backend слушает `127.0.0.1:8080`. Dashboard запускается отдельно
(`./dist/job-agent-dashboard`, `127.0.0.1:8081`). Для browser-операций
запустите worker: `cd browser-worker && npm ci && npm run build && npm start`.

## 3. Вход в HH

Через dashboard: секция «Вход в HH» → профиль `primary` → «Начать вход» →
e-mail и код из письма (или captcha). Сессия сохранится в browser storage
state профиля (`data/profiles/primary.json`) и переживёт перезапуск.

Через CLI (нужен запущенный browser worker):

```sh
job-agent auth login --profile primary --state-output ./data/profiles/primary.json
```

Если state уже есть (например, экспорт Playwright), используйте импорт:

```sh
job-agent auth import --source export.json \
  --state-output ./data/profiles/primary.json --force
```

Проверить статус профиля можно в dashboard или через `job-agent auth status
--profile primary --watch` (следит за сессией через SSE).

## 4. Первый поиск и dry-run отклик

Перезапустите backend после правок конфигурации и запустите job вручную из
dashboard (раздел «Задания» → «Запустить») или через API:

```sh
curl -sf -X POST -H "Authorization: Bearer $JOB_AGENT_API_TOKEN" \
  -H "Idempotency-Key: $(uuidgen)" \
  http://127.0.0.1:8080/api/v1/jobs/<job-tag>/runs
```

`<job-tag>` — тег job из конфигурации (в примере —
`daily-backend-applications`). Смотрите результат в dashboard («Отклики»,
«Очередь», «Задания») или через API:

```sh
curl -s -H "Authorization: Bearer $JOB_AGENT_API_TOKEN" \
  "http://127.0.0.1:8080/api/v1/applications?limit=20"
```

В `dry_run` отклики остаются в состоянии подготовки: проверьте письмо,
decision reason и фильтры. Если вакансия требует анкету, в разделе «Проверки
и опросники» появится сессия — заполните её, и отправка продолжится
автоматически.

## 5. Реальные отклики

В профиле:

```json
"applications": {
  "mode": "submit",
  "daily_limit": 30,
  "submit_jitter": {"min": "15s", "max": "30s"}
}
```

`daily_limit` — потолок откликов в день на профиль/платформу,
`submit_jitter` — пауза между отправками. Перезапустите сервис и начинайте с
небольших значений, проверяя dashboard после каждого запуска. Платформа может
сама остановить отправку (`rate_limited`, `quota_exceeded`) — job дождётся
разрешённого времени или остановится с понятной причиной.

## 6. Обслуживание

```sh
job-agent-check ./deploy/config.json               # ready | degraded | blocked
job-agent db backup --config ./deploy/config.json  # снимок SQLite
job-agent db restore --config ./deploy/config.json --input <backup> --force
```

Upgrade и restore выполняются на остановленном сервисе, после backup.
Типовые сбои и порядок восстановления — в [`runbook.md`](runbook.md).
