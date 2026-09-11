# Версии и выпуск

Версия продукта хранится в `buildinfo/VERSION` и следует SemVer. Пока внешние
контракты развиваются, Job Agent выпускается как `0.x.y`; текущая ветка —
`0.3.0-dev`. Префикс `v` используется только в Git tag (`v0.3.0`), а в JSON и
OCI label записывается чистая SemVer-строка (`0.1.0`). API имеет независимую
версию контракта `v1`, уже отражённую в путях `/api/v1/...`.

Одна сборка содержит backend, migration runner и dashboard. Версию можно
проверить без конфигурации:

```text
job-agent --version
job-agent-migrate --version
job-agent-dashboard --version
curl http://127.0.0.1:8080/api/v1/version
curl http://127.0.0.1:8081/dashboard-healthz
```

`make build` подставляет версию, commit, UTC build time и признак dirty tree во
все три бинаря. Docker принимает те же значения через build args; Compose —
через `JOB_AGENT_VERSION`, `JOB_AGENT_COMMIT`, `JOB_AGENT_BUILD_TIME` и
`JOB_AGENT_MODIFIED`. Image tag по умолчанию совпадает с dev-версией.

Первый release выполняется из чистого commit:

1. заменить `0.3.0-dev` в `buildinfo/VERSION` на `0.3.0` и синхронно передать
   `JOB_AGENT_VERSION=0.3.0` container build;
2. выполнить `make release-check` и smoke основных read/write policy без
   реальных нежелательных действий; отдельно запустить `job-agent-check` на
   deployment config: `degraded` требует разбора failed-задач, а `blocked`
   запрещает выпуск;
3. собрать image с commit SHA и RFC3339 build time, проверить версии трёх
   бинарей и endpoints;
4. создать annotated tag `v0.3.0`; публикация tag/image выполняется отдельно и
   только явно.

Перед `v1.0.0` должны стабилизироваться config schema, API v1, миграции с
проверенным upgrade/rollback, platform error semantics, idempotency/reconcile,
секреты и backup/restore. Наличие URL `/api/v1` само по себе не означает
готовность продукта `v1.0.0`.

Критерии `v1.0.0` (детальный маршрут — `docs/roadmap-v1.md`):

1. интерактивный browser login с чистого аккаунта и сохранением storage state;
2. живое прохождение vacancy test с записью evidence и повтором по сохранённым
   ответам;
3. создание и update резюме как desired state с publish и read-back;
4. замороженная схема конфига с include/glob и точными ошибками;
5. API v1 с Bearer-токеном либо явно зафиксированный single-user loopback
   контракт;
6. проверенные upgrade/rollback миграций, backup/restore и destructive guard;
7. зелёные `make verify` и `go test -race ./...`; LICENSE и quickstart;
8. `job-agent-check` без `blocked`, выпуск из чистого commit по процессу выше.
