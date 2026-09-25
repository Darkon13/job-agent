# Версии и выпуск

Версия продукта хранится в `buildinfo/VERSION` и следует SemVer. Выпуск —
`1.9.7` (tag `v1.9.7`); префикс `v` используется только в Git tag, а в JSON и
OCI label записывается чистая SemVer-строка. API имеет независимую версию
контракта `v1`, отражённую в путях `/api/v1/...`.

Известные ограничения `1.9.7`: создание резюме и `bootstrap.when:
missing_resume` реализуются для native API (OAuth) после выпуска; browser-cookie
профиль создавать резюме не умеет и сообщает `Unsupported`. Tailoring переписывает
«О себе» и навыки, но ещё не раздел «Опыт»; живой skill verification зависит от
платформенных лимитов аккаунта.

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

Release выполняется из чистого commit:

1. заменить версию в `buildinfo/VERSION` (например, `1.9.7`) и синхронно
   передать `JOB_AGENT_VERSION=1.0.0` container build;
2. выполнить `make release-check` и smoke основных read/write policy без
   реальных нежелательных действий; отдельно запустить `job-agent check` на
   deployment config: `degraded` требует разбора failed-задач, а `blocked`
   запрещает выпуск;
3. собрать image с commit SHA и RFC3339 build time, проверить версии трёх
   бинарей и endpoints;
4. создать annotated tag `v1.9.7` и запушить его: release workflow сам
   проверит совпадение тега с `buildinfo/VERSION`, соберёт бинарники под
   linux amd64/arm64, посчитает checksums и опубликует GitHub Release; image
   публикуется отдельно и только явно.

Архив релиза содержит `job-agent`, `job-agent-migrate`, `job-agent-dashboard`,
`compose.yaml` с версионным тегом образа, `.env.example` и примеры `deploy/`.
Операторские утилиты доступны подкомандами фасада: `job-agent check`,
`job-agent browser-state`, `job-agent auth`, `job-agent db`,
`job-agent startup`. Исторические бинарники остаются тонкими обёртками;
`trigger`, `approve`, `review`, `qualification`, импорт банка вопросов и
bootstrap пока убраны из фасада — соответствующие действия выполняет
dashboard.

Образы публикует workflow `Images` по тому же тегу: `ghcr.io/<owner>/job-agent`
и `ghcr.io/<owner>/job-agent-browser-worker` (slim, системный Chromium) с
тегами `<version>` и `latest`. Ручной запуск workflow принимает тег явно.

Перед `v1.0.0` должны стабилизироваться config schema, API v1, миграции с
проверенным upgrade/rollback, platform error semantics, idempotency/reconcile,
секреты и backup/restore. Наличие URL `/api/v1` само по себе не означает
готовность продукта `v1.0.0`.

Операционная проверка перед выпуском:

1. backup на чистом deployment и restore этого backup на копии с проверкой
   `job-agent check` и `/readyz`;
2. upgrade и rollback миграций на копии backup (`job-agent-migrate up`, затем
   `--steps 1 down`), destructive guard подтверждён;
3. `LICENSE`, `docs/quickstart.md` и `docs/runbook.md` соответствуют текущему
   поведению;
4. CI зелёный: `make verify` (включая `go test -race`), `make smoke` и
   проверки browser-worker.

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
8. `job-agent check` без `blocked`, выпуск из чистого commit по процессу выше.
