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

Тот же action вызывают `POST /api/v1/actions/apply-primary-about/runs` и dashboard. Startup reconciliation включается отдельной политикой, а не следует автоматически из наличия resource в конфиге: изменение ФИО или текста «О себе» не должно неожиданно примениться при обычном рестарте.

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
- Read-only операции могут идти параллельно, пока adapter не доказал конфликт с browser context.
- `plan` не равен `apply`; UI может показать diff без внешнего действия.
- Apply использует optimistic concurrency и прекращается, если remote state изменился после plan.
- `publish` и visibility являются отдельными флагами policy, а не побочным эффектом update.
- Неизвестное обязательное поле, новый enum, captcha или schema drift переводят задачу в review.
- External processor не получает shell, credentials или произвольный доступ к filesystem по умолчанию.

## Порядок реализации

1. Ввести registry/chain детерминированных text processors и provenance.
2. Добавить versioned `ProfileStateResource`, proposal и semantic diff без внешнего apply.
3. Сделать read/plan API и форму «О себе» в dashboard.
4. Реализовать HH adapter для read schema, update about и обязательного read-back.
5. Подключить `profile.apply` task, per-profile mutation lane и manual run.
6. Обобщить update на остальные profile/resume fields по живой HH schema.
7. Добавить named actions, cron/event/API triggers.
8. Реализовать employer rule sets, policy routing и explain evidence.
9. Подключить external/model processors с limits и fallback.
