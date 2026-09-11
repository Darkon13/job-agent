# Browser worker: RPC-контракт

Статус: active (M1 roadmap v1). Worker — тонкий сервис браузерной механики.
Бизнес-логика, селекторы и разбор HTML остаются в Go: worker не знает про HH,
резюме, тесты и auth-flow.

## Границы

```text
Go backend (adapters, workflows)
        |  HTTP JSON, Bearer token
        v
browser-worker (TypeScript + Playwright)
        |  persistent BrowserContext per profile
        v
/data/browser-profiles/<profile_id>   (cookies, storage state, cache)
```

Worker умеет только:

- держать один Chromium и изолированный context на профиль;
- открывать страницы, кликать, заполнять поля, ждать;
- отдавать HTML/текст, снимок viewport или элемента;
- экспортировать/импортировать Playwright storage state.

Worker не выполняет произвольный JavaScript, не ходит в HH API и не принимает
селекторы как «скрипт». Ошибки платформы и policy-решения принимает Go.

## Транспорт

- Base URL: `http://browser-worker:8088` в Compose, `http://127.0.0.1:8088` локально.
- Все тела — JSON; снимок отдаётся как `image/png`.
- Заголовок `Authorization: Bearer <token>`; токен приходит из
  `BROWSER_WORKER_TOKEN` (secret volume). `/healthz` и `/readyz` без токена.
- Протокол версионирован: каждый ответ содержит `X-Browser-Protocol: 1`, а
  `healthz`/`readyz` дополнительно дублируют его в JSON; Go-клиент отказывается
  работать с другой версией.
- Предельный размер ответа Go-клиента: 8 MiB (PNG), 4 MiB (HTML).

### Ошибки

```json
{"error": {"code": "timeout", "message": "locator wait timed out"}}
```

| Код | HTTP | Категория в Go |
|---|---|---|
| `invalid` | 400 | `permanent_failure` |
| `unauthorized` | 401 | `unauthorized` |
| `not_found` | 404 | `permanent_failure` |
| `busy` | 409 | `conflict` |
| `timeout` | 504 | `temporary_failure` |
| `unsupported` | 400 | `unsupported` |
| `internal` | 500 | `temporary_failure` |

Транспортная ошибка или `5xx` в Go нормализуется как `temporary_failure`.

## Конкурентность

- Один Chromium process; один persistent context на профиль.
- Операции одного профиля сериализуются: Go держит per-profile lock, worker
  дополнительно отвечает `busy` (409), если операция не дождалась за
  `wait_ms` (по умолчанию 1000).
- У context одна активная страница; `page.goto` переиспользует её. Открытие
  дополнительных страниц не входит в v1.
- Лимит одновременных операций задаётся `BROWSER_WORKER_MAX_IN_FLIGHT`
  (по умолчанию 4) и не влияет на последовательность одного профиля.

## Операции v1

### Служебные

```text
GET /healthz                     -> {"status":"ok","version":"...","protocol":1}
GET /readyz                      -> {"status":"ready","browser":true,"contexts":N}
```

### Context lifecycle

```text
POST   /v1/profiles/{profile}/ensure
       body {"headless": true}
       -> {"profile_id":"...","created":true,"pages":1,"headless":true}

GET    /v1/profiles
       -> {"profiles":[{"profile_id":"...","pages":1,"headless":true}]}

DELETE /v1/profiles/{profile}?purge=false
       -> {"closed":true,"purged":false}
```

`ensure` идемпотентен. `purge=true` удаляет каталог профиля; по умолчанию
данные остаются на диске для следующего запуска.

### Storage state

```text
GET /v1/profiles/{profile}/storage-state
    -> Playwright storage state: {"cookies":[...],"origins":[...]}

PUT /v1/profiles/{profile}/storage-state
    body {"cookies":[...],"origins":[...]}
    -> {"imported":true}
```

Экспорт возвращает полный state; Go сам санитизирует его до HH-доменов,
пишет атомарно с `0600` и не логирует значения. Импорт заменяет context
(закрывает текущий) и применяет переданный state.

### Page primitives

```text
POST /v1/profiles/{profile}/goto
     body {"url":"https://hh.ru/...","wait_until":"domcontentloaded","timeout_ms":30000}
     -> {"url":"...","status":200,"title":"..."}

GET  /v1/profiles/{profile}/page
     -> {"url":"...","title":"...","has_page":true}

POST /v1/profiles/{profile}/content
     body {"timeout_ms":10000}
     -> {"html":"...","url":"..."}

POST /v1/profiles/{profile}/screenshot
     body {"selector":".captcha","full_page":false}
     -> image/png

POST /v1/profiles/{profile}/locator
     body {"action":"click|fill|press|wait","selector":"...","value":"...",
           "state":"visible|attached|hidden","timeout_ms":10000}
     -> {"action":"click","matched":true}
```

Правила:

- `goto.wait_until`: `load`, `domcontentloaded`, `networkidle`;
- `locator.wait.state`: обязателен только для `wait`;
- `fill` очищает поле и вводит значение; секреты не логируются;
- `press` принимает имя клавиши Playwright (`Enter`, `Escape`, ...);
- `timeout_ms` ограничен сверху `BROWSER_WORKER_MAX_TIMEOUT_MS` (по умолчанию
  120000);
- `screenshot.selector` пустой означает viewport; элемент делается visible
  и не масштабируется.

## Хранение и сеть

- Каталог профилей: `BROWSER_WORKER_DATA_DIR` (по умолчанию `/data/browser-profiles`).
- Каталог создаётся с правами `0700`, файлы Playwright остаются локальными.
- Headed-режим для VNC включается `BROWSER_WORKER_HEADLESS=0` и `DISPLAY=:1`;
  per-profile override `headless` в `ensure` имеет приоритет.
- Worker не публикует порт на host в Compose; Go обращается по внутренней сети.

## Проверки

- Go-контрактные тесты работают против in-memory fake `browser/browsertest`.
- Unit-тесты worker проверяют валидацию запросов, лимиты таймаутов и маппинг
  ошибок без запуска Chromium.
- E2E smoke выполняется на машине владельца через общий VNC: ensure → goto
  `hh.ru` → screenshot → экспорт state. Живой login проверяет M2.
