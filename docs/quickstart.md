# Quickstart: чистый аккаунт

Этот маршрут поднимает Job Agent с нуля: пустой `data/`, новый HH-аккаунт без
сохранённых cookies и первая кампания откликов. Предполагается Linux или macOS
с Docker и Compose v2; сам backend на хосте не обязателен.

## 1. Конфигурация

Создайте рабочий каталог:

```text
job-agent/
|- deploy/
|  |- config.json
|  |- profiles.json
|  `- searches/golang.json
`- data/
```

`deploy/config.json` — единственный файл с `database`/`server`/`include`:

```json
{
  "schema_version": 1,
  "database": {"driver": "sqlite", "path": "/data/job-agent.db"},
  "server": {
    "listen": "127.0.0.1:8080",
    "api_token_env": "JOB_AGENT_API_TOKEN"
  },
  "include": ["profiles.json", "searches/*.json"]
}
```

`deploy/profiles.json` описывает профиль и адаптер:

```json
{
  "adapters": [{"tag": "hh-main", "type": "hh"}],
  "profiles": [
    {
      "tag": "primary",
      "adapter": "hh-main",
      "state_file": "/data/profiles/primary.state.json",
      "enabled": true,
      "applications": {"mode": "dry_run"}
    }
  ]
}
```

Начните с `"mode": "dry_run"`: подготовка и письма выполняются, платформа не
меняется. Реальный submit включается после проверки письма и лимитов.

`deploy/searches/golang.json` добавляет поиск:

```json
{
  "searches": [
    {
      "tag": "golang",
      "adapter": "hh-main",
      "profiles": ["primary"],
      "query": {"source": "global", "text": "Golang developer", "area": ["1"]}
    }
  ]
}
```

## 2. Запуск

Задайте токен API и при необходимости ключ модели:

```bash
export JOB_AGENT_API_TOKEN="$(openssl rand -hex 32)"
export OPENAI_API_KEY="..."          # только если включён model operator
```

Поднимите сервисы (browser worker нужен для интерактивного входа):

```bash
JOB_AGENT_CONFIG_DIR=./deploy JOB_AGENT_DATA_DIR=./data \
  docker compose --profile browser up -d --build
```

Проверка:

```bash
curl -sf -H "Authorization: Bearer $JOB_AGENT_API_TOKEN" \
  http://127.0.0.1:8080/api/v1/version
curl -sf http://127.0.0.1:8081/dashboard-healthz
```

## 3. Вход в HH

Интерактивный вход выполняет CLI на хосте и управляет browser worker:

```bash
BROWSER_WORKER_URL=http://127.0.0.1:8088 \
BROWSER_WORKER_TOKEN=... \
  job-agent auth login --profile primary --state-output ./data/profiles/primary.state.json
```

Команда попросит e-mail, затем код или captcha в терминале. После успеха CLI
сохраняет browser storage state с правами `0600`; backend подхватывает его без
перезапуска.

Если state уже есть (например, экспорт Playwright), используйте офлайн-импорт:

```bash
job-agent auth import --source export.json \
  --state-output ./data/profiles/primary.state.json --force
```

## 4. Первый поиск и отклик

Перезапустите backend после изменения конфигурации и запустите поиск через
расписание или вручную:

```bash
curl -sf -X POST -H "Authorization: Bearer $JOB_AGENT_API_TOKEN" \
  -H "Idempotency-Key: $(uuidgen)" \
  http://127.0.0.1:8080/api/v1/jobs/golang/run
```

Смотрите состояние в dashboard (`JOB_AGENT_DASHBOARD_BIND_IP` по умолчанию
`127.0.0.1:8081`) или через API:

```bash
curl -s -H "Authorization: Bearer $JOB_AGENT_API_TOKEN" \
  "http://127.0.0.1:8080/api/v1/applications?limit=20"
```

`dry_run` оставляет отклики в состоянии подготовки. Проверьте письмо и
decision reason, затем смените политику и включите реальный submit.

## 5. Обслуживание

```bash
job-agent-check --config ./deploy/config.json     # ready|degraded|blocked
job-agent db backup --config ./deploy/config.json # снимок SQLite
job-agent db restore --config ./deploy/config.json --input <backup> --force
```

Восстановление и upgrade выполняются на остановленном сервисе. Перед
обновлением image всегда делайте backup; подробности — в `docs/runbook.md`.
