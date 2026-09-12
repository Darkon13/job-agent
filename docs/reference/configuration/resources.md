# `resources`

Декларативный desired state профиля: объявленные поля резюме и личных данных,
которые сервис приводит к нужному виду через безопасный план `plan → apply →
read-back`. Значения не попадают в публичный diff: наружу отдаются только
JSON Pointer и digests.

## Пример

```json
"resources": [
  {
    "tag": "main-about",
    "type": "profile_state",
    "profile": "primary",
    "ownership": "declared_fields",
    "state": {
      "resumes": {
        "0123456789abcdef": {
          "web": {"skills": "Backend-инженер на Go..."}
        }
      }
    }
  }
]
```

| Поле | Тип | Обяз. | Описание |
|---|---|---|---|
| `tag` | string | **да** | Имя ресурса; на него ссылаются `jobs[].action.resource`. |
| `type` | string | **да** | Сейчас `profile_state`. |
| `profile` | string | **да** | `tag` профиля-владельца. |
| `ownership` | string | **да** | `declared_fields`: изменяются только объявленные поля, остальные не читаются и не перезаписываются. |
| `state` | object | **да** | Документ desired state: JSON Pointer-пути объявленных полей. |

## Семантика

- наличие ресурса в конфиге **не** запускает apply: нужен job
  `profile_state.reconcile`, ручной API-вызов или dashboard;
- immutable proposal хранит desired state и digests; перед записью выполняется
  сравнение с свежим наблюдением (optimistic concurrency);
- после записи обязателен read-back; несовпадение переводит apply в
  `recovery_required`;
- подтверждённый apply становится redacted revision (before/after digests,
  paths, source, `applied_at`) и доступен через
  `GET /api/v1/profile-state/revisions`;
- `profile_state.apply` имеет системный максимальный приоритет и удерживает
  барьер откликов профиля до завершения (в том числе после `failed` до явного
  retry/dismiss).

Формат путей и доступные поля описаны в
[Desired state](../../profile-desired-state-and-routing.md) и
[HH resume contract](../../hh-resume-contract.md); bootstrap — в
[Bootstrap](../../profile-bootstrap.md).

!!! warning "Ещё не реализовано"
    Registry переиспользуемых named actions и event triggers пока не
    реализованы: resource приводит состояние только через собственные
    reconcile/apply-цепочки. Также поддерживается только
    `ownership: declared_fields`.
