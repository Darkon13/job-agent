# CLI и фасад `job-agent`

Один бинарник `job-agent` запускает сервис и содержит все операторские
утилиты подкомандами. Исторические бинарники (`job-agent-check`,
`job-agent-trigger`, `job-agent-approve`, `job-agent-browser-state`,
`job-agent-question-bank-import`) остались тонкими обёртками и поддерживаются,
но в архивах релиза поставляются только фасад, `job-agent-migrate` и
`job-agent-dashboard`.

`job-agent help` (он же `--help`) печатает список подкоманд; неизвестная
команда или несуществующий конфиг тоже показывают эту справку.

## Запуск сервиса

| Команда | Назначение |
|---|---|
| `job-agent [-migrate-up] <config.json>` | backend, scheduler и HTTP API; `-migrate-up` применяет миграции перед стартом |
| `job-agent-migrate [-config FILE] up\|down\|version\|force` | миграции отдельным бинарником |
| `job-agent-dashboard` | dashboard и same-origin proxy к backend API |

## Диагностика и обслуживание

| Команда | Назначение |
|---|---|
| `job-agent check <config.json>` | проверяет конфиг, наличие ключей моделей, схему БД и очередь задач; `blocked` запрещает выпуск |
| `job-agent db backup -config FILE [-output FILE]` | согласованный снимок БД (`VACUUM INTO`) на остановленном сервисе |
| `job-agent db restore -config FILE --input BACKUP [--force]` | восстановление из снимка; без `--force` файл не перезаписывается |
| `job-agent browser-state sanitize <state.json>` | оставляет только домены платформы и выставляет права `0600` |

## Операции

| Команда | Назначение |
|---|---|
| `job-agent auth login --profile <tag> [--state-output FILE]` | вход в HH с сохранением browser state |
| `job-agent auth import --source export.json --state-output FILE [--force]` | импорт сохранённого состояния |
| `job-agent auth status --session <id> [--watch]` | статус интерактивной сессии |
| `job-agent auth logout --profile <tag>` | выход и очистка сессии |
| `job-agent startup ...` | однократная стартовая сверка профиля |

Запуск job вручную, проверки и опросники, каталог тестов, импорт банка
вопросов и bootstrap профиля выполняются в dashboard — CLI-формы для них
временно убраны, потому что оказались неудобными и часть ещё не готова.
Исторические бинарники (`job-agent-trigger`, `job-agent-approve`,
`job-agent-question-bank-import`) при сборке из исходников по-прежнему
собираются и работают.

## Подключение к работающему сервису

`auth`, `review` и `qualification` ходят в HTTP API и по умолчанию используют
`http://127.0.0.1:8080`. При запуске через Compose backend наружу не
публикуется — передайте `--api http://127.0.0.1:8081` (dashboard-прокси) и тот
же токен, что у сервиса (`JOB_AGENT_API_TOKEN`).

Команды, работающие с файлами (`check`, `db`, `trigger`, `approve`), читают
конфиг и базу напрямую. В контейнере их удобно запускать через уже описанные
в `compose.yaml` тома:

```sh
docker compose run --rm job-agent /usr/local/bin/job-agent \
  check /config/config.json
docker compose run --rm job-agent /usr/local/bin/job-agent \
  trigger -idempotency-key "$(date +%s)" /config/config.json daily-applications
```

## Переменные окружения CLI

| Переменная | Зачем | Обязательность |
|---|---|---|
| `BROWSER_WORKER_TOKEN` | общий секрет backend'а и browser-worker: воркер управляет Chromium с авторизованными сессиями, каждый RPC подписан токеном | только с профилем `browser` |
| `JOB_AGENT_API_TOKEN` | Bearer-токен HTTP API; защищает запуск откликов и переписку от доступа извне loopback, используется CLI и dashboard-прокси | только для внешнего API и CLI |
| `OPENAI_API_KEY`, `DEEPSEEK_API_KEY` | model-операторы для писем и ответов; без них работают шаблоны и банк | только при объявленных `models` |

Backend без browser-воркера и без публикации API запускается с пустыми
значениями: loopback-режим не требует токена, а browser-операции просто
недоступны до его появления.

Оба секрета можно создать не только файлом: `scripts/init-env.sh` запишет
`.env`, `scripts/init-env.sh --print` напечатает готовые строки, а совсем
вручную значение даёт `openssl rand -hex 32` — одна команда на токен.
