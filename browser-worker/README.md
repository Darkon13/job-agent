# browser-worker

Опциональный сервис на TypeScript и официальном Playwright. Он выполняет
browser-only операции, которых нет в API платформ: вход в HH, чтение и
заполнение анкет и тестов, чаты, поднятие резюме, activity telemetry.
Бизнес-логика остаётся в Go-backend; worker — тонкий RPC-исполнитель.

Контракт операций (`goto`, `content`, `screenshot`, `locator`, `ensure`,
`storage-state` и другие), DTO, ошибки и требования к безопасности описаны в
[`../docs/browser-rpc.md`](../docs/browser-rpc.md).

## Запуск

```sh
npm ci
npm run build
BROWSER_WORKER_TOKEN="$(openssl rand -hex 32)" npm start
```

Worker слушает `127.0.0.1:8088` и требует `Authorization: Bearer <token>` на
каждом запросе. Backend обращается к нему через `BROWSER_WORKER_URL` и
`BROWSER_WORKER_TOKEN`.

## Переменные окружения

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `BROWSER_WORKER_TOKEN` | — (обязательна) | Bearer-токен RPC |
| `BROWSER_WORKER_HOST` | `127.0.0.1` | адрес listener |
| `BROWSER_WORKER_PORT` | `8088` | порт listener |
| `BROWSER_WORKER_DATA_DIR` | `/data/browser-profiles` | каталог profile contexts |
| `BROWSER_WORKER_HEADLESS` | `true` | headless-режим Chromium |
| `BROWSER_WORKER_MAX_IN_FLIGHT` | `4` | лимит одновременных операций |
| `BROWSER_WORKER_MAX_TIMEOUT_MS` | `120000` | потолок таймаута операции |
| `BROWSER_WORKER_CHANNEL` | — | Playwright channel (`chrome` и т.п.) |
| `BROWSER_WORKER_EXECUTABLE` | — | путь к системному браузеру, если Playwright не находит его сам |

## Разработка

```sh
npm ci
npm run typecheck
npm test          # сборка + unit-тесты HTTP-поверхности и конфигурации
```

Правила контрибуции и границы проекта — в
[`../CONTRIBUTING.md`](../CONTRIBUTING.md).
