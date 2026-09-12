# Desired state профиля, processors и policy routing

## Задача

Профиль и резюме должны изменяться одинаково независимо от источника команды: конфиг, cron, ручной REST-запрос или dashboard. Источник не вызывает HH напрямую. Он выбирает именованное действие, после чего обычный durable workflow читает текущее состояние, строит план, применяет его и проверяет результат.

```text
config | cron | REST | dashboard
              |
              v
       named action: profile.apply
              |
              v
read current -> processors -> desired state -> plan/diff -> policy
                                                     |
                                                     v
                                        durable profile.apply task
                                                     |
                                                     v
                                  adapter apply -> read-back -> audit
```

Это похоже на `kubectl apply` по направлению «описанное состояние → фактическое состояние», но не копирует Kubernetes API. HH и другие платформы имеют динамические схемы, словари и обязательность полей, поэтому adapter остаётся владельцем platform-specific validation и patch.

## Ресурс desired state

Целевой конфигурационный объект:

```json
{
  "resources": [
    {
      "tag": "primary-backend-profile",
      "type": "profile_state",
      "profile": "primary",
      "ownership": "declared_fields",
      "state": {
        "profile": {
          "first_name": "Иван"
        },
        "resumes": {
          "backend": {
            "title": "Backend developer",
            "about": {
              "processor": "about-backend"
            }
          }
        }
      }
    }
  ]
}
```

`ownership: declared_fields` означает:

- отсутствующее поле не меняется;
- явный `null` запрашивает очистку, если adapter и platform schema это разрешают;
- scalar заменяется целиком;
- массив заменяется целиком, если конкретный adapter не объявил другую merge-семантику;
- неизвестное поле отвергается до постановки внешней задачи;
- read-back должен подтвердить каждое поле, которым владеет ресурс.

Полный platform payload не кладётся в task и обычный audit, поскольку профиль содержит персональные данные. Перед применением создаётся persistent proposal: manifest tag, content digest, защищённая ссылка на snapshot, список изменённых JSON paths, semantic diff и expected remote revision/hash. Retry продолжает ту же proposal revision, а не перечитывает уже изменившийся конфиг.

## Именованные actions и triggers

Resource описывает состояние, action — способ и политику его применения, trigger — момент запуска:

```json
{
  "actions": [
    {
      "tag": "apply-primary-about",
      "type": "profile.apply",
      "resource": "primary-backend-profile",
      "approval": "on_destructive_change",
      "publish": false
    }
  ],
  "jobs": [
    {
      "tag": "rotate-primary-about",
      "enabled": true,
      "triggers": [{"type": "cron", "expression": "0 10 * * 1", "timezone": "Europe/Moscow", "misfire": "run_once"}],
      "concurrency": "forbid",
      "action": "apply-primary-about"
    }
  ]
}
```

В целевом контракте тот же action вызывают
`POST /api/v1/actions/apply-primary-about/runs` и dashboard. Startup
reconciliation включается отдельной политикой, а не следует автоматически из
наличия resource в конфиге: изменение ФИО или текста «О себе» не должно
неожиданно примениться при обычном рестарте.

Текущий промежуточный контракт уже поддерживает inline action job:

```json
{
  "tag": "reconcile-primary-about",
  "enabled": true,
  "triggers": [{"type": "cron", "expression": "0 10 * * 1", "timezone": "Europe/Moscow", "misfire": "run_once"}],
  "concurrency": "forbid",
  "action": {"type": "profile_state.reconcile", "resource": "primary-backend-profile"}
}
```

Scheduler, `job-agent-trigger`, dashboard и
`POST /api/v1/profile-state/resources/{tag}/reconcile` создают одну и ту же
durable task. Она хранит только `resource_tag`, выполняет trusted read, строит
immutable proposal и идемпотентно ставит apply; при уже совпавшем состоянии
внешнего изменения нет. Отдельный registry переиспользуемых `actions` и
универсальный `/actions/{tag}/runs` остаются следующим обобщением.

Плановое обновление резюме использует отдельный job action:

```json
{
  "tag": "refresh-primary-about",
  "enabled": true,
  "triggers": [{"type": "cron", "expression": "0 10 * * *", "timezone": "Europe/Moscow", "misfire": "run_once"}],
  "concurrency": "forbid",
  "action": {"type": "resume.update", "profile": "primary", "resource": "primary-backend-profile", "publish": false}
}
```

Cron создаёт обычный durable `resume.update` task: worker читает declared
resource, строит immutable proposal, применяет его с обязательным read-back и
при `publish: true` повторяет публикацию до успеха. Подтверждение не требуется:
если платформа отвечает cooldown-ом, задача перепланируется на
`Retry-After`/`next_publish_at`, а scheduler не создаёт второй запуск, пока
первый активен. `resume`, если не задан, берётся из профиля; профиль без
reader/writer, а для `publish: true` и без publisher, job пропускает.

## Processor — преобразователь, а не обязательно AI

Processor получает типизированные данные и возвращает преобразованные данные с provenance. Минимальный контракт:

```go
type Processor interface {
    Process(ctx context.Context, input Input) (Output, error)
}
```

Первый набор реализаций:

- `text.template` — детерминированный шаблон;
- `text.replace` — последовательность literal/regexp замен;
- `text.blocks` — выбрать, переставить, добавить или удалить именованные блоки строки;
- `json.merge` — декларативно собрать объект из текущего состояния и patch;
- `chain` — последовательно выполнить несколько processors;
- `model` — вызвать LLM и затем обязательный validator;
- `external` — узкая программа или отдельный service с JSON stdin/stdout, argv без shell, timeout, лимитом размера и явной allowlist.

LLM является одной реализацией processor, а не владельцем pipeline. Простая утилита, которая переставляет блоки «О себе», проходит через тот же контракт, diff и retry. Processor не выполняет внешнее изменение: side effect принадлежит только adapter action после validation/policy.

Output хранит tag и версию processor, digest входа/выхода, warnings и изменённые paths. Внутреннее reasoning модели, credentials и полный чувствительный snapshot в audit не попадают.

## Rule sets и routing по модели sing-box

Из sing-box переносится структура, а не сетевые имена:

```text
sing-box rule_set -> Job Agent rule_sets: переиспользуемые факты/группы
sing-box rules    -> Job Agent policy rules: упорядоченные match-условия
sing-box action   -> Job Agent action tag: route/skip/review/apply/notify
sing-box final    -> Job Agent explicit fallback action
```

Пример групп работодателей и маршрута:

```json
{
  "rule_sets": [
    {
      "tag": "marketplaces",
      "type": "employer",
      "rules": [
        {"platform": "hh", "employer_id": "2180"},
        {"name": "Ozon"},
        {"include": "wildberries-companies"}
      ]
    }
  ],
  "policies": [
    {
      "tag": "application-routing",
      "rules": [
        {"rule_set": ["blocked-employers"], "action": "skip"},
        {"rule_set": ["marketplaces"], "action": "apply-marketplace"},
        {"title_contains": ["Go", "Golang"], "action": "apply-go"}
      ],
      "final": "apply-backend-default"
    }
  ]
}
```

Правила проверяются сверху вниз. Внутри одного простого правила разные поля образуют `AND`, значения одного поля — `OR`; сложные `and/or/not` оформляются логическим правилом. Совпадение возвращает evidence (`employer_id`, exact alias, domain, nested set), поэтому UI может объяснить выбранный action. Rule set не содержит side effect и может храниться inline, в локальном файле или позже в подписанном remote source с digest/cache.

Термин `outbound` не используется: в Job Agent результатом может быть не направление трафика, а `skip`, manual review, выбор сопроводительного, processor chain, `profile.apply` или уведомление.

## Границы безопасности и конкурентности

- На профиль действует одна mutation lane: ФИО, резюме, publish и touch не меняются конкурентно.
- `profile_state.apply` имеет системный максимальный приоритет и является
  барьером для `application.submit` того же профиля: ожидающий apply сначала
  должен завершиться успешно. `failed` остаётся закрытым барьером до явного
  retry либо `dismissed`. Ожидание отклика не увеличивает attempts; уже
  выполняющийся внешний запрос не прерывается.
- Read-only операции могут идти параллельно, пока adapter не доказал конфликт с browser context.
- `plan` не равен `apply`; UI может показать diff без внешнего действия.
- Apply использует optimistic concurrency и прекращается, если remote state изменился после plan.
- `publish` и visibility являются отдельными флагами policy, а не побочным эффектом update.
- Неизвестное обязательное поле, новый enum, captcha или schema drift переводят задачу в review.
- External processor не получает shell, credentials или произвольный доступ к filesystem по умолчанию.

## Реализованная граница

Внутренний read/plan-фундамент уже реализован:

- конфиг принимает `resources` типа `profile_state` и проверяет ссылки на профиль;
- state канонизируется, ограничивается по размеру и вложенности и использует ownership `declared_fields`;
- semantic diff возвращает только JSON Pointer, операцию и digests, не публикуя значения;
- immutable proposal хранит точный desired snapshot внутри SQLite, но исключает его из публичного JSON;
- одинаковые manifest + observed state + remote revision дедуплицируются после restart;
- SQLite migration 11 и memory repository реализуют одинаковый repository port;
- planner принимает только observation от доверенного adapter reader;
- HH browser reader делает только `GET` и сейчас полностью поддерживает путь
  `/resumes/{external-id}/about`; неподдерживаемый набор полей отвергается без
  частичного observation;
- read/plan API публикует metadata ресурсов и redacted proposals, а при
  создании плана сам вызывает reader. Прислать фактический state в теле запроса
  нельзя;
- явный apply endpoint ставит durable `profile_state.apply` task, содержащий
  только `proposal_id`; worker получает неизменяемый desired snapshot из
  SQLite;
- подтверждённый apply пишет durable redacted revision (миграция 29):
  before/after digests, список изменённых JSON Pointer paths, source и
  applied_at без значений полей; `GET /api/v1/profile-state/revisions`
  отдаёт историю с фильтрами, а dashboard показывает её по ресурсу. Ревизия
  идемпотентна по proposal, повторная запись сохраняет первый applied_at;
- HH browser writer поддерживает `about`, проверяет все declared paths до POST
  и подтверждает результат повторным GET. Состояние, не совпадающее ни с
  before, ни с after digest, завершается конфликтом;
- частично выполненный multi-resume plan безопасно продолжается с оставшихся
  полей; потерянный ответ POST сначала сверяется read-back;
- dashboard даёт раздельные команды plan/apply и показывает только redacted
  список операций;
- dashboard может загрузить разрешённое поле `about`, собрать one-shot override
  и построить для него обычный immutable proposal. Override хранится только в
  proposal и не переписывает декларативный resource; пустой текст означает
  явное `null`/очистку. API принимает только уже объявленные редактируемые пути
  и требует digest базового manifest, поэтому устаревшая форма не применяется;
- cron, ручной CLI и API/dashboard trigger могут поставить durable
  `profile_state.reconcile`; payload содержит только tag ресурса, а worker
  выполняет read → plan → enqueue apply и ничего не меняет при `no_changes`;
- profile-level `bootstrap.source` разрешается относительно config, проверяется
  как versioned `ProfileBootstrap` и после авторизации проходит trusted read →
  условие `empty` → immutable plan → durable apply до запуска workers. Если
  заполнен хотя бы один объявленный путь, manifest целиком пропускается;
  `missing_resume` и автоматический publish пока fail-fast отклоняются;
- `profile_state.apply`, `resume.touch` и `application.submit` проходят через
  одну process-local mutation lane на `profile_id`: один профиль изменяется
  последовательно, а разные профили могут исполняться параллельно. Claim
  отклика дополнительно исключает профиль с активным либо последним не
  разрешённым failed `profile_state.apply`, поэтому отдельные type-filtered
  workers не обходят приоритет apply. Явные `/retry` и `/dismiss` соответственно
  открывают новый bounded retry-cycle или снимают устаревший барьер. Текущий
  Compose-контракт допускает ровно один backend с mutating workers; перед
  горизонтальным масштабированием lane нужно заменить общей lease/lock в
  durable storage.

В текущем формате ключ `resumes` является внешним ID/hash резюме. Именованные
локальные aliases появятся вместе с отдельным каталогом resume targets.

Ссылки вида `{"processor":"about-backend"}` пока намеренно отвергаются как
неразрешённые. Сначала config builder должен выполнить processor и передать в
core итоговое значение. В dashboard редактируется только уже объявленный
`about`; остальные поля профиля в этом срезе отсутствуют. Простое наличие
resource в конфиге не вызывает side effect.

## Порядок реализации

1. ✅ Ввести registry/chain детерминированных text processors и provenance.
2. ✅ Добавить versioned `ProfileStateResource`, proposal и semantic diff без внешнего apply.
3. ✅ Сделать доверенный HH read поля `about` и read/plan API.
4. ✅ Добавить plan/apply controls в dashboard поверх API.
5. ✅ Реализовать HH adapter для update about и обязательного read-back.
6. ✅ Подключить durable `profile_state.apply` task и явный API run.
7. ✅ Объединить apply и touch в общую per-profile mutation lane; подключить к
   ней publish при появлении этого worker.
8. ✅ Добавить редактирование desired «О себе» в dashboard без подмены source of truth.
9. ✅ Обобщить update на остальные profile/resume fields по живой HH schema.
10. Частично: cron, ручной CLI и API/dashboard reconcile готовы; вынести inline
    actions в переиспользуемый registry и добавить event triggers.
11. Частично: подключить config-declared bootstrap для `when: empty`; добавить
    `missing_resume` только вместе с create-resume action и отдельный publish.
12. Реализовать employer rule sets, policy routing и explain evidence.
13. Подключить external/model processors с limits и fallback.
