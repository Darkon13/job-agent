# `jobs`

Расписание. Cron, доменное событие или ручной вызов создают durable task;
логику выполняет обычный worker, а не сам trigger. Поэтому у
запланированного, ручного и event-запуска одинаковые проверки, audit и retry.

## Пример

```json
"jobs": [
  {
    "tag": "touch-primary-resume",
    "enabled": true,
    "concurrency": "forbid",
    "triggers": [
      {"type": "cron", "expression": "0 */4 * * *"}
    ],
    "action": {"type": "resume.touch", "profile": "primary"}
  }
]
```

Джоба обычно состоит из выражения и действия: `timezone`, `misfire` и `jitter`
подставляются по умолчанию. Явные значения всегда побеждают, а
`"jitter": {"min": "0s", "max": "0s"}` выключает разброс.

| Поле | Тип | Обяз. | По умолчанию | Описание |
|---|---|---|---|---|
| `tag` | string | **да** | — | Уникальное имя job; используется в API/CLI-запуске и idempotency key. |
| `enabled` | bool | **да** | — | Выключенный job не создаёт определения расписания. |
| `priority` | number | нет | `0` | Приоритет задач, `-1000..1000`. Дочерние задачи workflow наследуют его. |
| `triggers` | array | **да** | — | Хотя бы один trigger (см. ниже). |
| `concurrency` | string | **да** | — | Сейчас поддерживается только `forbid`: второй экземпляр не запускается, пока первый активен. |
| `action` | object | **да** | — | Действие при срабатывании (см. ниже). |

## `triggers[]`

| Поле | Тип | Обяз. | По умолчанию | Описание |
|---|---|---|---|---|
| `type` | string | **да** | — | Поддерживается `cron`. |
| `expression` | string | **да** | — | Cron-выражение в стандартном синтаксисе (5 полей). |
| `timezone` | string | нет | `Europe/Moscow` | IANA-таймзона, например `Europe/Moscow`. |
| `misfire` | string | нет | `run_once` | Пропущенный запуск выполняется один раз. Другое значение отклоняется. |
| `jitter` | object | нет | `1m..10m` | Случайная задержка запуска: `min`, `max` (Go duration). Явный `0s..0s` выключает разброс. |

Те же значения подставляет редактор джоб в дашборде, поэтому job, созданная из
UI, ведёт себя как объявленная в конфиге.

Для cron idempotency key включает `tag` и канонический scheduled time, поэтому
двойной запуск одной минуты не создаёт две задачи.

## `action`

| Поле | Тип | Обяз. | Описание |
|---|---|---|---|
| `type` | string | **да** | Тип действия (см. таблицу ниже). |
| `profile` | string | зависит от типа | `tag` профиля. |
| `profiles` | array | зависит от типа | Массив `tag` профилей с общим расписанием; взаимоисключающе с `profile`. Job раскрывается в одно срабатывание на профиль. |
| `resume` | string | нет | Резюме (ID или alias); по умолчанию — `profiles[].resume`. |
| `resource` | string | для `resume.update`, `profile_state.reconcile` | `tag` из `resources[]`. |
| `publish` | bool | нет | Опубликовать результат `resume.update` после apply. |
| `profiles` | array | для `application.campaign` | Профили рассылки. |
| `routes` | array | для `application.campaign` | `tag` поисков (routes), по порядку. |
| `target_successful` | number | для `application.campaign` | Сколько успешных откликов нужно рассылке. |
| `max_in_flight` | number | для `application.campaign` | Сколько откликов одновременно «в работе» (рекомендуется 1–3). |
| `follow_up` | object | для `conversation.follow_up.select` | Параметры выбора диалога. |
| `retention` | object | для `application.retention` | Параметры очистки. |

### Типы действий

| `type` | Поля | Что делает |
|---|---|---|
| `resume.touch` | `profile`, `resume`? | Поднимает резюме. Платформа сама сообщает `nextTouchAt`; задача откладывается до разрешённого времени (HH — раз в 4 часа). |
| `resume.publish` | `profile`, `resume` | Публикует/перепубликует резюме, при cooldown ждёт `Retry-After`. |
| `resume.update` | `profile`, `resource`, `publish`? | plan → apply → read-back для декларативного ресурса. |
| `profile.activity.observe` | `profile`, `resume`? | Снимок статистики резюме (read-only). |
| `profile.session_refresh` | `profile` | Перечитывает cookies из live browser-контекста, оставляет HH-домены и атомарно обновляет `state_file` (`0600`). Требует `state_file` у профиля. |
| `profile_state.reconcile` | `resource` | Сверяет declared resource с платформой и при drift создаёт apply. |
| `conversation.sync` | `profile` | Синхронизирует каталог и историю чатов. |
| `conversation.follow_up.select` | `profile`, `follow_up` | Выбирает один диалог и планирует durable follow-up таймер. |
| `application.campaign` | `profiles`, `routes`, `target_successful`, `max_in_flight` | Розыгрыш откликов по routes до цели. |
| `application.retention` | `profile`, `retention` | Локальная очистка старых откликов. |
| `application.state.sync` | `profile` | Read-only чтение состояний откликов (браузерная сессия) и обновление карточек в dashboard. |
| `profile.activity.maintain` | `profiles`, `count`, `pause` | Просмотр реальных вакансий-кандидатов браузерной сессией для поддержания активности; ничего не отправляет. |

### `conversation.follow_up.select`

```json
"follow_up": {
  "strategy": "oldest_unanswered",
  "all": true,
  "minimum_silence": "24h",
  "run_after": "72h",
  "deadline_after": "168h",
  "content": {"text": "Здравствуйте! Уточню, актуальна ли ещё вакансия?"},
  "policy": {"cancel_on_incoming": true, "require_active_conversation": true, "max_follow_ups": 2, "cooldown": "120h"}
}
```

| Поле | Тип | Обяз. | Описание |
|---|---|---|---|
| `strategy` | string | **да** | `oldest_unanswered`, `newest_unanswered`, `random` (стабильный для task). |
| `minimum_silence` | duration | **да** | Минимальная тишина до выбора диалога. |
| `run_after` | duration | **да** | Через сколько после последнего исходящего планировать отправку. |
| `deadline_after` | duration | нет | Предельный срок жизни таймера. |
| `content` | object | **да** | Ровно один источник: `text`, `template` (tag пула) или `operator`. |
| `policy` | object | **да** | `cancel_on_incoming`, `require_active_conversation`, `max_follow_ups`, `cooldown`. |

Выбор никогда не превращает входящее сообщение, ожидающее ответа соискателя,
в автоматическое напоминание.

### `application.state.sync`

Периодическое read-only чтение списка откликов из кабинета HH по браузерной
сессии профиля. Обновляет `application_platform_states` (состояние, приглашение,
отказ, скрытие и признак просмотра), по которым dashboard показывает группы
откликов. Ничего не отправляет и не удаляет: чистка остаётся отдельной политикой
`application.retention`. Пропущенные профили без браузерной сессии просто
остаются без наблюдений.

### `application.retention`

| Поле | Тип | Обяз. | Описание |
|---|---|---|---|
| `stale_after` | duration | **да** | Возраст отклика, после которого его можно удалить. |
| `remove_rejected` | bool | нет | Удалять также отклики с подтверждённым отказом; по умолчанию `false`. |

Автоматическая очистка требует свежего platform state и повторной проверки перед
удалением; приглашения доступны только для ручного select→action. Подробности —
в [Очистка откликов](../../application-cleanup.md).

!!! warning "Не реализовано для browser-cookie профилей"
    Наблюдатель статусов откликов работает через OAuth/API. Для профилей
    только с browser-сессией автоматический GC недоступен — удаление
    выполняется вручную через dashboard/API.

!!! note "Тестируется"
    `profile.session_refresh` проверен живьём: обновляет cookies из браузерного
    контекста, поддерживает сессию, но не воскрешает уже разлогиненный аккаунт.

## Системные джобы

Синхронизация состояния, обновление сессии, перепроверка анкет, подъём резюме,
поддержание активности и очистка откликов не описываются в `jobs`: сервис
генерирует их из политик профиля (`state_harvest`, `resume_touch`,
`activity_maintain`, `application_cleanup` — см.
[profiles](profiles.md#системные-джобы-профиля)). Теги начинаются с `system.` и
видны в дашборде и CLI отдельной группой.

Разброс запуска системной джобы по умолчанию не превышает десятой части её
интервала и пяти минут, поэтому частый опрос остаётся пунктуальным, а редкие
задачи не сходятся в одну минуту. Политика профиля принимает явный `jitter`,
если нужно другое окно.

`system.activity-maintain` по умолчанию открывает вакансии из общего фида
(`{"source":"global"}`), потому что метрике нужны карточки для реального
просмотра, а не узкий поиск профиля. Поле `activity_maintain.query` заменяет
источник, если оператор хочет ограничить его своим поиском.
