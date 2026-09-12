# `server`

HTTP API и наблюдаемость. Backend по умолчанию слушает loopback; публикация
порта разрешена только с Bearer-токеном.

## Пример

```json
"server": {
  "listen": "127.0.0.1:8080",
  "exposure": "loopback",
  "api_token_env": "JOB_AGENT_API_TOKEN",
  "follow_up_reconcile_interval": "30s",
  "scheduler_reconcile_interval": "15s"
}
```

| Поле | Тип | Обяз. | По умолчанию | Описание |
|---|---|---|---|---|
| `listen` | string | нет | `127.0.0.1:8080` | Адрес и порт API. Loopback, если `api_token_env` пуст. |
| `exposure` | string | нет | `loopback` | `loopback` — только локальный интерфейс; `private` — непубличный сетевой интерфейс (например, адрес в контейнерной сети или туннеле). Публичный `0.0.0.0` требует токена. |
| `api_token_env` | string | нет | — | Имя переменной окружения с Bearer-токеном. Backend принимает запросы только с `Authorization: Bearer <token>`. Dashboard пробрасывает токен server-side и не отдаёт его браузеру. |
| `follow_up_reconcile_interval` | string | нет | `30s` | Период восстановления due follow-up таймеров после сбоя. |
| `scheduler_reconcile_interval` | string | нет | `15s` | Период пересчёта расписания jobs. |

## Что отдаёт API

- `GET /healthz`, `GET /readyz` — liveness/readiness;
- `GET /api/v1/version` — version/commit/build time/modified;
- `GET /api/v1/dashboard/summary` — сводка для dashboard;
- `GET /api/v1/applications`, `/jobs`, `/tasks/failed`, `/conversations`,
  `/profile-state/*`, `/review-sessions`, `/answer-blocks`,
  `/profiles/{profile}/qualifications` — operator API;
- `POST /api/v1/jobs/{tag}/runs`, `/applications/{id}/retry`,
  `/tasks/{id}/retry|dismiss`, `/profile-state/*` — мутации с
  `Idempotency-Key`;
- `/metrics` — Prometheus text за тем же Bearer-токеном.

## Наблюдаемость

- входящий `X-Request-ID` сохраняется, при отсутствии генерируется;
- на каждый запрос — одна структурная access-log строка;
- shutdown по `SIGINT`/`SIGTERM` с ограниченным grace period.

## Примечания

- Публикация портов backend на host не требуется: dashboard общается с ним по
  внутренней сети (Compose) или через reverse proxy.
- `exposure: private` не защищает от доступа внутри этой сети: ставьте токен,
  если сеть не доверенная.
