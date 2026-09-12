# Шпаргалки и рецепты

Готовые фрагменты конфигурации: копируйте нужный блок в свой config и правьте
значения. Все примеры — части одного JSON, а не отдельные файлы; полное
описание ключей — в [`configuration.md`](configuration.md).

Обозначения: `jsonc` — фрагмент конфига с комментариями, `sh` — команды
оболочки, `json` — самостоятельный файл.

## Несколько HH-аккаунтов

Каждый аккаунт — отдельный профиль со своим резюме, сессией и лимитами.
Отклики не пересекаются: ключ — профиль/вакансия.

```jsonc
"profiles": [
  {
    "tag": "backend",                       // произвольное имя профиля
    "adapter": "hh-main",                   // ссылка на adapters[].tag
    "resume": "resume-id-backend",          // ID резюме или alias
    "state_file": "/data/profiles/backend.json",
    "applications": {
      "mode": "submit",                     // dry_run | approval | submit
      "daily_limit": 30,                    // откликов в день на профиль
      "submit_jitter": {"min": "15s", "max": "30s"},
      "message_template_file": "messages/backend.json",
      "timezone": "Europe/Moscow"
    }
  },
  {
    "tag": "golang",
    "adapter": "hh-main",
    "resume": "resume-id-golang",
    "state_file": "/data/profiles/golang.json",
    "applications": {
      "mode": "dry_run",                    // этот аккаунт только готовит письма
      "message_template_file": "messages/backend.json",
      "timezone": "Europe/Moscow"
    }
  }
]
```

Что принимает:

| Поле | Значения |
|---|---|
| `tag` | любое уникальное имя, используется в `profiles[]` поисков и jobs |
| `mode` | `dry_run` — без отправки, `approval` — ждёт подтверждения, `submit` — отправляет |
| `daily_limit` | целое ≥ 1; лимит на профиль/платформу в сутки |
| `submit_jitter` | `min`/`max` — Go duration (`15s`, `1m`) |
| `message_template_file` | путь к файлу пула писем относительно config |

Поиск и рассылка перечисляют профили по `tag`:

```jsonc
"searches": [
  {
    "tag": "golang",
    "adapter": "hh-main",
    "profiles": ["backend", "golang"],
    "query": {"source": "global", "text": "Golang developer", "area": ["1"]}
  }
],
"jobs": [
  {
    "tag": "daily-applications",
    "triggers": [
      {"type": "cron", "expression": "30 9 * * *", "timezone": "Europe/Moscow"}
    ],
    "action": {
      "type": "application.campaign",
      "profiles": ["backend", "golang"],
      "routes": ["golang"],
      "target_successful": 40,
      "max_in_flight": 2
    }
  }
]
```

## Цепочка поисков с fallback

Если глобальная выдача закончилась, а цель по откликам не достигнута,
рассылка переходит к следующему маршруту.

```jsonc
"searches": [
  {
    "tag": "golang-global",
    "adapter": "hh-main",
    "profiles": ["main"],
    "priority": 100,                  // порядок обхода в рассылке
    "target_applications": 20,        // сколько вакансий собрать из поиска
    "fallback": "golang-similar",     // следующий search.tag
    "query": {
      "source": "global",
      "text": "Golang developer",
      "area": ["1"],                  // 1 — Москва, см. api.hh.ru/areas
      "page_size": 20,                // вакансий на страницу
      "max_pages": 2                  // предел пагинации за проход
    }
  },
  {
    "tag": "golang-similar",
    "adapter": "hh-main",
    "profiles": ["main"],
    "priority": 50,
    "query": {
      "source": "similar_resume",     // похожие на это резюме
      "resume": "resume-id",
      "area": ["1"]
    }
  }
]
```

Что принимает `query.source`: `global`, `similar_resume` (нужен `resume`),
`similar_vacancy` и `related_vacancy` (нужен `vacancy`). Остальные фильтры —
в [`configuration.md`](configuration.md#searches).

## Поднятие резюме

`resume.touch` поднимает резюме в выдаче. Платформа сама сообщает, когда
следующее поднятие разрешено: задача откладывается до `nextTouchAt`.

```jsonc
"jobs": [
  {
    "tag": "touch-resume",
    "enabled": true,
    "triggers": [
      {
        "type": "cron",
        "expression": "0 10 * * *",       // ежедневно в 10:00
        "timezone": "Europe/Moscow",
        "misfire": "run_once",            // догнать один пропущенный запуск
        "jitter": {"min": "5m", "max": "30m"}
      }
    ],
    "concurrency": "forbid",
    "action": {"type": "resume.touch", "profile": "main"}
  }
]
```

| Поле trigger | Значения |
|---|---|
| `expression` | cron из 5 полей |
| `timezone` | IANA-зона, например `Europe/Moscow` |
| `misfire` | `run_once` — один догоняющий запуск |
| `jitter` | случайная задержка старта, `min`/`max` |
| `concurrency` | `forbid`, `forbid_per_profile`, `allow` |

## Обновление «О себе» из конфига

Desired state: объявляете поля в `resources`, а job `resume.update` выполняет
plan → apply → read-back и, если нужно, публикацию. Значения не хранятся в
задаче — только immutable proposal с digest.

```jsonc
"resources": [
  {
    "tag": "main-about",
    "type": "profile_state",              // пока только этот тип
    "profile": "main",
    "ownership": "declared_fields",       // владеет лишь объявленными листьями
    "state": {
      "resumes": {
        "resume-id": {
          "web": {                        // web | web_profile (browser-профиль)
            "skills": ["Backend-разработчик на Go. Люблю простые решения и честные тесты."]
          }
        }
      }
    }
  }
],
"jobs": [
  {
    "tag": "update-about",
    "enabled": true,
    "triggers": [
      {"type": "cron", "expression": "0 10 * * 1", "timezone": "Europe/Moscow"}
    ],
    "concurrency": "forbid_per_profile",
    "action": {
      "type": "resume.update",
      "profile": "main",
      "resource": "main-about",
      "publish": true                     // опубликовать после apply
    }
  }
]
```

Комментарий: так обновляются объявленные текстовые поля резюме. Массовый
рерайт достижений моделью и перестановка блоков опыта проектируются в
[`../application-resume-tailoring.md`](../application-resume-tailoring.md).

## Подстройка резюме под вакансию перед откликом

Временная адаптация резюме: сервис снимает snapshot, добавляет подобранные
навыки, отправляет отклик и возвращает резюме как было. При внешнем
изменении резюме во время подстройки — `recovery_required`, а не перезапись.

```jsonc
"profiles": [
  {
    "tag": "main",
    "adapter": "hh-main",
    "resume": "resume-id",
    "state_file": "/data/profiles/main.json",
    "applications": {
      "mode": "submit",
      "message_template_file": "messages/backend.json",
      "tailoring": {
        "skills": {
          "enabled": true,                // включить подбор навыков
          "maximum": 8,                   // сколько навыков максимум
          "model": {                      // необязательно: выбор через ИИ
            "provider": "openai",
            "prompt_version": "v1",
            "instruction": "Выбери навыки из вакансии, которых не хватает в резюме",
            "timeout": "30s"
          }
        },
        "about": {                        // необязательно: переписать «О себе»
          "enabled": true,
          "maximum_runes": 600,           // контекст резюме берётся из аккаунта
          "model": {                      // обязателен; resume_facts_file не нужен
            "provider": "openai",
            "prompt_version": "v1",
            "instruction": "Подчеркни опыт, важный для этой вакансии",
            "timeout": "30s"
          }
        }
      }
    }
  }
]
```

Без `model` выбор делает детерминированная политика. Подробности и границы —
в [`../application-resume-tailoring.md`](../application-resume-tailoring.md).

## Модель для сопроводительных

Формат пула, доступные поля шаблона и стратегии выбора описаны в
[Письмах и пулах](messages.md). Провайдер модели и policy задаются у профиля.
Модель получает обезличенный контекст: вместо личных данных — плейсхолдеры,
реальные значения подставляются перед отправкой. Обязателен fallback-пул писем, чтобы при сбое модели письмо всё
равно ушло.

```jsonc
"models": [
  {
    "tag": "openai",                     // имя провайдера для ссылок
    "type": "openai_responses",          // тип провайдера
    "model": "gpt-5-mini",               // имя модели
    "api_key_env": "OPENAI_API_KEY",     // переменная с ключом
    "max_output_tokens": 800
  }
],
"profiles": [
  {
    "tag": "main",
    "adapter": "hh-main",
    "resume": "resume-id",
    "resume_facts_file": "facts/main.json",
    "state_file": "/data/profiles/main.json",
    "applications": {
      "mode": "submit",
      "message_template_file": "messages/backend.json",   // обязательный fallback
      "model": {
        "provider": "openai",            // ссылка на models[].tag
        "prompt_version": "v1",
        "instruction": "Короткое сопроводительное по фактам резюме и вакансии",
        "timeout": "30s"
      }
    }
  }
]
```

Если вместо OpenAI используется DeepSeek или другой endpoint с chat
completions, укажите `type: "openai_chat"` и свой `base_url`; structured output
там работает через JSON mode и тот же локальный валидатор:

```jsonc
"models": [
  {
    "tag": "deepseek",
    "type": "openai_chat",
    "model": "deepseek-chat",
    "base_url": "https://api.deepseek.com/v1",
    "api_key_env": "DEEPSEEK_API_KEY",
    "max_output_tokens": 1024
  }
]
```

`facts/main.json` — только явные факты, которые модели разрешено
использовать:

```json
{
  "tag": "main-facts",
  "resume_id": "resume-id",
  "digest": "sha256:<digest>",
  "facts": {
    "grade": "senior",
    "years": 6,
    "stack": ["Go", "PostgreSQL", "Kafka"],
    "placeholders": {
      "name": "Иван",
      "surname": "Иванов",
      "phone": "+7 900 000-00-00"
    }
  }
}
```

Плейсхолдеры из `facts.placeholders` и работодатель (`{company_name}`) не
попадают в контекст модели: она видит только `{name}`-токены, а реальные
значения подставляются локально перед сохранением письма. Неизвестный
плейсхолдер в ответе модели уводит подготовку в fallback-письмо.

Digest считается по каноническому JSON `{"resume_id": ..., "facts": ...}`:

```sh
python3 - <<'PY'
import hashlib, json

resume_id = "resume-id"
facts = {"grade": "senior", "years": 6, "stack": ["Go", "PostgreSQL", "Kafka"]}
canonical = json.dumps(
    {"resume_id": resume_id, "facts": facts},
    ensure_ascii=False,
    separators=(",", ":"),
    sort_keys=True,
)
print("sha256:" + hashlib.sha256(canonical.encode()).hexdigest())
PY
```

Что подставляется перед отправкой: `{name}`, `{surname}`, `{company_name}` и
другие плейсхолдеры из объявленного набора. Неизвестный или незаполненный
плейсхолдер останавливает отправку — лучше fallback-письмо, чем утечка или
пустая строка. Контракт контекста описан в
[`../next-cover-letter-routing.md`](../next-cover-letter-routing.md).

## Автоответы на опросники в чатах

Вручную можно отвечать кнопками в dashboard. Для автоматических ответов
включите `answer_known` и положите ответы в `answer_sets`. Отвечаются только
вопросы, для которых есть reviewed-ответ; незнакомые ждут вас.

```jsonc
"profiles": [
  {
    "tag": "main",
    "adapter": "hh-main",
    "state_file": "/data/profiles/main.json",
    "conversations": {
      "allow_send": true,        // право отправки сообщений
      "allow_mark_read": true,   // право менять read-state
      "answer_known": true       // автоответы по answer_sets
    }
  }
],
"answer_sets": ["answers/conversation/invitation.json"]
```

Файл ответа — один вопрос на блок:

```json
{
  "tag": "invitation",
  "name": "Приглашение: посмотрю вакансию",
  "kind": "conversation",
  "platform": "hh",
  "match": {
    "topic": "Ответьте на приглашение, даже если оно вам не интересно. Так мы сможем рекомендовать вам более подходящие вакансии. Отправить ответ можно одной кнопкой:"
  },
  "answers": [
    {
      "question": "Ответьте на приглашение, даже если оно вам не интересно. Так мы сможем рекомендовать вам более подходящие вакансии. Отправить ответ можно одной кнопкой:",
      "selected_options": ["Посмотрю вакансию, спасибо"]
    }
  ]
}
```

Значения: `match.topic` — точный текст вопроса (нормализуется по пробелам и
регистру) либо `match.fingerprint`; `selected_options` — точные тексты
кнопок. Ответы содержат личные данные — держите файлы в `data/` вне Git.

## Автопрогон анкет вакансий

Когда анкета встречается повторно, reviewed-блок заполняется автоматически:
capture находит вопросы, сопоставляет по тексту и fingerprint и ставит
отправку. Новые вопросы останавливают отклик в «Проверках и опросниках».

```json
{
  "tag": "hh-vacancy-reviewed",
  "name": "Анкеты вакансий",
  "kind": "vacancy",
  "platform": "hh",
  "answers": [
    {"question": "У Вас есть Высшее образование?", "selected_options": ["Да"]},
    {"question": "Какой формат работы для себя рассматриваешь?", "selected_options": ["Удаленка"]}
  ]
}
```

Значения: `kind: vacancy` не использует matcher; `question` — текст вопроса,
`selected_options` — точные тексты выбранных вариантов; для открытых вопросов
вместо `selected_options` используется `text`.

## Напоминание рекрутеру

Job выбирает один подходящий диалог (последнее сообщение — исходящее,
ответа нет) и планирует одноразовый follow-up. Требуется `allow_send`.

```jsonc
"jobs": [
  {
    "tag": "remind-employer",
    "enabled": true,
    "triggers": [
      {"type": "cron", "expression": "15 11 * * 1-5", "timezone": "Europe/Moscow"}
    ],
    "concurrency": "forbid_per_profile",
    "action": {
      "type": "conversation.follow_up.select",
      "profile": "main",
      "follow_up": {
        "strategy": "oldest_unanswered",       // oldest_unanswered | newest_unanswered | random
        "minimum_silence": "72h",              // сколько ждать ответа
        "run_after": "1m",                     // когда отправить после выбора
        "deadline_after": "24h",               // отменить, если не отправлено
        "content": {"text": "Добрый день! Актуальна ли ещё вакансия?"},
        "policy": {
          "cancel_on_incoming": true,          // входящее отменяет отправку
          "require_active_conversation": true,
          "max_follow_ups": 1,
          "cooldown": "72h"
        }
      }
    }
  }
]
```

## Очистка старых откликов

Локальное удаление без платформенного DELETE: остаётся tombstone для dedup,
а приглашения защищены от удаления.

```jsonc
"jobs": [
  {
    "tag": "cleanup",
    "enabled": true,
    "triggers": [
      {"type": "cron", "expression": "0 3 * * *", "timezone": "Europe/Moscow"}
    ],
    "concurrency": "forbid_per_profile",
    "action": {
      "type": "application.retention",
      "profile": "main",
      "retention": {
        "stale_after": "336h",        // 14 дней
        "remove_rejected": true       // удалять и отказы
      }
    }
  }
]
```

## Разрешить смену видимости резюме

По умолчанию отклик останавливается с причиной
`resume_visibility_change_required`, если работодатель требует видимость
«всем». Разрешите изменение явно:

```jsonc
"applications": {
  "mode": "submit",
  "allow_visibility_change": true
}
```

## Ручной запуск и проверки

```sh
job-agent-check ./deploy/config.json
curl -s -H "Authorization: Bearer $JOB_AGENT_API_TOKEN" \
  http://127.0.0.1:8080/readyz
curl -s -X POST -H "Authorization: Bearer $JOB_AGENT_API_TOKEN" \
  -H "Idempotency-Key: $(uuidgen)" \
  http://127.0.0.1:8080/api/v1/jobs/daily-applications/runs
```

`job-agent-check` возвращает `ready`, `degraded` или `blocked`; последний
запрещает новые отклики, пока failed `profile_state.apply` держит барьер.

## Backup и восстановление

```sh
job-agent db backup  --config ./deploy/config.json
job-agent db restore --config ./deploy/config.json --input <backup> --force
```

Выполняйте на остановленном сервисе и после backup перед обновлением.
