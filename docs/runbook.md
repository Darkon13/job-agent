# Runbook: восстановление и типовые сбои

Операционные процедуры для одного инстанса Job Agent. Перед любым изменением
конфигурации или image сделайте backup базы.

## Backup и restore

```bash
job-agent db backup --config ./deploy/config.json
job-agent db restore --config ./deploy/config.json --input job-agent.db.backup-20260911T120000Z --force
```

- `db backup` делает согласованный снимок через `VACUUM INTO`, проверяет
  integrity и schema version; существующий файл не перезаписывается.
- `db restore` принимает только валидную базу этой же schema line, требует
  `--force` для замены и удаляет устаревшие `-wal`/`-shm`.
- Восстановление выполняйте на остановленном сервисе. После restore запустите
  `job-agent-check --config ./deploy/config.json` и проверьте `/readyz`.

Проверка backup без восстановления:

```bash
job-agent db backup --config ./deploy/config.json --output /tmp/check.db
# schema=<N> в выводе и отсутствие ошибок достаточно для процедуры
```

## Upgrade и rollback

1. Остановите сервис.
2. Сделайте backup.
3. Обновите image или бинари.
4. Примените миграции: `job-agent-migrate --config ./deploy/config.json up`.
5. Запустите сервис, проверьте `/readyz` и `job-agent-check`.

Rollback выполняется только явным числом шагов и только на backup, снятый до
upgrade:

```bash
job-agent-migrate --config ./deploy/config.json --steps 1 down
```

Миграции с destructive guard (например, application GC) не откатываются без
предварительно снятого backup.

## auth_required

Симптом: профиль помечен `auth_required`, задачи профиля не выполняются.

1. Проверьте `job-agent auth status` или dashboard.
2. Обновите вход: `job-agent auth login --profile <tag> --state-output <state>`
   при поднятом browser worker, либо `job-agent auth import` для готового
   storage state.
3. После сохранения state backend поднимет профиль сам; при необходимости
   повторите failed-задачи через API явным retry.

## recovery_required (resume tailoring)

Симптом: отклик с временной подстройкой резюме остановлен, следующий tailoring
того же профиля не стартует.

1. Найдите активную tailoring saga и её состояние (dashboard или API
   `/api/v1/profile-state/...`; см. `docs/application-resume-tailoring.md`).
2. Проверьте платформенное состояние резюме: внешний drift требует ручного
   решения, автоматическая перезапись не выполняется.
3. После ручного согласования снимите барьер явным retry либо dismiss задачи;
   failed `profile_state.apply` держит барьер откликов до явного решения.

## Failed profile_state.apply

`job-agent-check` возвращает `blocked`, пока последний apply профиля failed.
Не запускайте новые отклики: сначала разберите причину (proposal ID, digest
drift, ошибка write transport), затем retry или dismiss через API. Повторный
apply безопасен: proposal immutable, adapter проверяет before/after digests и
делает read-back.

## Quota и rate limit

- `quota_exceeded` останавливает рассылку до сброса окна; дождитесь
  `retry_after` из ошибки, не создавайте сотни delayed tasks.
- `rate_limited` откладывает конкретную операцию; ручной retry возможен после
  указанного времени.
- Дневной лимит профиля проверяется и адаптером, и бюджетом рассылки.

## Resume hash изменился

Симптом: `profile.activity.observe` падает с
`configured resume activity statistics were not found`, `resume.touch`
возвращает `permanent_failure: configured resume was not found`, а отклики
получают `resume_not_suitable`.

HH выдаёт резюме новый hash после существенного редактирования, поэтому
`profiles[].resume` в конфиге перестаёт указывать на живое резюме. Проверьте
`applicantResumes[]._attributes.hash`/`latestResumeHash` в
`https://hh.ru/applicant/profile/me` (initial state) и обновите `resume` либо
алиас в `resume_aliases`, затем перезапустите backend. Уже созданные delayed
touch-задачи со старым hash стоит дождаться/закрыть: новые cron-запуски
возьмут обновлённый ID.

## Сессия браузера устарела

Симптом: browser-backed чтения или записи возвращают `permanent_failure` вида
«HH browser resource is not accessible for this account», `activity.observe`
не находит статистику, хотя раньше всё работало; в логе старта профиль
получает контакты из config, а не из платформы.

Причина: backend работает по снимку `state_file`, а HH периодически
переставляет cookies. Снимок нужно обновлять из живого browser-контекста.

Штатная страховка — job `profile.session_refresh` (например, каждые 4 часа):
он берёт cookies из browser worker, оставляет только HH-домены и атомарно
перезаписывает `state_file` с правами `0600`. Запустить вручную:

```bash
job-agent-trigger -idempotency-key refresh-primary-$(date +%s) config.json refresh-primary-session
```

Если и в browser worker сессия мертва (worker отвечает `unauthorized`),
перелогиньтесь: `job-agent auth login --profile primary` или VNC-поток, затем
импорт state. После обновления файла перезапуск backend не требуется: браузерные
транспорты читают `state_file` перед каждой операцией.

## Поиск: изменение конфига

`search_runs` хранит cursor и закреплённое определение поиска (adapter,
platform, профиль, target profiles и канонический `query`). Если у существующего
`tag` поменять `query`, старый cursor относится к другой выдаче, поэтому
сервис **автоматически** начинает новую generation: обновляет определение,
очищает cursor и `done`, увеличивает `generation` и `revision` и пишет в лог
`search "<tag>" configuration changed; generation N starts with an empty
cursor`. Участие оператора не требуется, дедупликация вакансий и откликов не
даёт повторных действий.

Версия тега (`golang-search` → `-v2`) остаётся полезной, только если нужно
сохранить оба определения как отдельные поиски с независимыми курсорами.
Ручное удаление строки `search_runs` больше не требуется; для аварийных случаев
сначала сделайте `job-agent db backup`.

## Docker на малом VPS

Штатный Compose-стек требует много места: образ воркера на базе Playwright весит
около 2.5 ГБ. Для сервера с небольшим диском используйте облегчённые образы:

- `browser-worker/Dockerfile.slim` — `node:22-slim` + системный Chromium из
  Debian и `BROWSER_WORKER_EXECUTABLE=/usr/bin/chromium`; образ ~1.3 ГБ вместо
  ~2.5 ГБ. Playwright при этом запускает системный браузер.
- `Dockerfile.binary` — упаковывает уже собранные локально статические
  бинарники в distroless. Нужен там, где сборка Go внутри Docker не имеет
  доступа к module proxy или у сервера мало CPU.

Типовой сценарий:

```bash
# на рабочей машине
make build
docker build -t job-agent:1.1.0 -f Dockerfile.binary \
  --build-arg VERSION=1.1.0 --build-arg COMMIT=$(git rev-parse --short=12 HEAD) .
docker save job-agent:1.1.0 | gzip -1 | ssh server 'gunzip | docker load'

# на сервере
docker build -f browser-worker/Dockerfile.slim -t job-agent-browser-worker:slim .
docker compose up -d
```

Config/data монтируются как `/config` и `/data`; dashboard слушает
`127.0.0.1:18081` и проксирует API, backend порт наружу не публикуется.

## Runtime lease

Симптом: при старте `another job-agent instance already holds the runtime
lease`.

Это single-replica guard: остановите предыдущий процесс. Lease переживает
падение не дольше 90 секунд; после этого новый запуск подхватит его
автоматически. Второй живой инстанс на той же SQLite запрещён.

## API token

Токен задаётся через `server.api_token_env` (имя переменной, не значение).
Ротация: задайте новое значение переменной и перезапустите backend и dashboard
одновременно — оба читают токен из окружения при старте. Health-пробы
(`/healthz`, `/readyz`) остаются без токена.

## Browser worker недоступен

Симптом: browser-операции и интерактивный вход возвращают `unauthorized` или
`temporary_failure`.

1. Проверьте `docker compose --profile browser ps` и `/healthz` worker-а.
2. Убедитесь, что `BROWSER_WORKER_URL` и `BROWSER_WORKER_TOKEN` совпадают у
   backend и worker.
3. API-only операции (поиск, отклики по OAuth, publish) продолжают работать;
   browser-only (login, тесты) откладываются до восстановления worker-а.
