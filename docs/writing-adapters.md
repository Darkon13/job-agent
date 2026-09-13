# Разработка адаптера платформы

Адаптер — это единственное место, которое знает о конкретной платформе
(HeadHunter, другой агрегатор). `core`, `workflow` и `storage` не содержат
platform-specific типов: они работают с capability-интерфейсами из пакета
`adapter`.

Этот файл — практический чек-лист: границы слоёв, нормализованные ошибки,
задачи и browser worker описаны здесь же, ниже.

## Границы

```text
cmd/job-agent           composition root: config → registry → workflows → workers
core/                   доменные типы, ошибки, задачи, questionnaire, review
adapter/                контракты: capability-интерфейсы, команды, результаты
adapters/<platform>/    фасад платформы: API client, browser client, mapper, auth
workflow/               прикладные сценарии поверх контрактов
worker/                 обработчики durable задач (тонкие, вызывают adapters)
config/                 декларативная конфигурация и валидация
storage/                repositories (SQLite + in-memory для тестов)
browser-worker/         опциональный TypeScript/Playwright RPC для browser-only функций
```

Правила:

- одна платформа — один пакет `adapters/<platform>/`;
- адаптер сам выбирает транспорт (`api`, `browser`, `auto`), внешний workflow не
  знает, через что выполнено действие;
- ошибки нормализуются в `core.ErrorCategory`, а не в platform-specific коды;
- никакой `init()`-регистрации: фабрика явно подключается в `cmd/job-agent`.

## Какой минимум нужен новому адаптеру

Реализуйте только те интерфейсы из `adapter/`, которые платформа реально
поддерживает. Проверка `capabilities` идёт через type assertion, поэтому
неподдерживаемая операция должна давать `core.ErrorUnsupported`, а не панику.

Основные capability:

| Интерфейс | Назначение |
|---|---|
| `VacancySearcher` / `VacancyReader` | поиск и чтение вакансий |
| `SuitableResumeReader` | список подходящих резюме до отклика |
| `ApplicationTransport` | отправка отклика и его результат |
| `ApplicationStateObserver` / `ApplicationReconciler` | наблюдение статусов и сверка неоднозначных исходов |
| `ConversationTransport` / `ConversationDiscoverer` | чаты: история, отправка, mark-read, каталог |
| `VacancyTestCapturer` / `VacancyTestSubmitter` | анкеты и тесты на странице вакансии |
| `ResumeToucher` / `ResumePublisher` | поднятие и публикация резюме |
| `ProfileActivityObserver` / `ProfileReader` | активность и данные профиля |
| `ProfileStateReader` / `ProfileStateWriter` | desired state профиля (read → plan → apply → read-back) |
| `QualificationCatalogReader` / `QualificationAttemptService` | каталог и попытки skill verification |
| `BrowserSessionBinder` и `Browser*SessionBinder` | привязка browser-сессий профиля |

Полный список и точные сигнатуры — в `adapter/adapter.go` и
`adapter/qualification.go`.

## Ошибки

Каждая операция возвращает `*core.OperationError` с одной из категорий:

- `Unsupported` — операция недоступна этим транспортом; разрешён fallback;
- `Unauthorized` — нужно обновить сессию или токен;
- `RateLimited` / `QuotaExceeded` — отложить задачу (`RetryAfter`) или
  остановить рассылку;
- `ValidationRequired` / `ConfirmationRequired` — нужен человек или анкета;
- `TemporaryFailure` — ограниченный retry;
- `PermanentFailure` — повтор без изменения входа бессмысленен;
- `AmbiguousResult` — исход внешнего действия неизвестен, нужна сверка.

Сообщение должно быть безопасным: без токенов, cookies, текстов резюме и
персональных данных. `Metadata` — короткие машинные коды (`code`, `reason`).

## Тесты

- Контрактные операции проверяйте через `httptest`-фикстуры адаптера
  (`adapters/hh/*_test.go`) — без реальной сети.
- Fakes для workflow-тестов лежат в `adapter/browsertest` и `worker/*_test.go`;
  не подменяйте поведение production-кода в тестах ради удобства.
- Для browser-функций используйте RPC-контракт `docs/browser-rpc.md` и fake из
  `browser/browsertest`.
- Прогон: `make verify` (включает `go test -race ./...`, `go vet`,
  `git diff --check`). Для runtime-пути добавьте `make smoke`.
- Любой новый HTTP-контракт платформы фиксируйте в `docs/<platform>-*.md`
  с датой наблюдения и точными полями запроса/ответа.

## Config и секреты

Объекты платформы объявляются в декларативном config и валидируются вашим
адаптером. Секреты не попадают в config, логи, task payload и audit: только
`credentials_ref`, browser storage state или переменные окружения.

## Интеграция

1. `adapters/<platform>` с фабрикой `func New(settings json.RawMessage) (adapter.Adapter, error)`;
2. регистрация фабрики в `cmd/job-agent` (и в `cmd/job-agent-approve`, если
   адаптер поддерживает approve-путь);
3. adapter-specific блок в `config/` с валидацией;
4. `docs/<platform>-<area>.md` с живыми контрактами;
5. тесты и, если нужно, новый worker в `worker/` + регистрация task type.

## Checklist pull request

- [ ] `make verify` зелёный, worktree без несвязанных изменений
- [ ] новые операции возвращают нормализованные категории ошибок и не текут
      секретами в сообщениях
- [ ] есть unit-тесты на success и failure-ветки
- [ ] живые контракты зафиксированы в `docs/`
- [ ] обновлены `docs/changelog.md` и затронутые страницы вики
- [ ] описано, что осталось непроверенным живьём
