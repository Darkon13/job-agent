# Шпаргалки и рецепты

Готовые куски конфигурации, которые можно копировать в свой config. Каждый
рецепт самодостаточен; полные описания ключей — в
[`configuration.md`](configuration.md).

## Несколько HH-аккаунтов

Каждый аккаунт — отдельный профиль со своим резюме, сессией и лимитами.
Отклики не пересекаются: ключ — профиль/вакансия.

```json
"profiles": [
  {
    "tag": "backend",
    "adapter": "hh-main",
    "resume": "resume-id-backend",
    "state_file": "/data/profiles/backend.json",
    "applications": {
      "mode": "submit",
      "daily_limit": 30,
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
      "mode": "dry_run",
      "message_template_file": "messages/backend.json",
      "timezone": "Europe/Moscow"
    }
  }
]
```

Поиск и кампания просто перечисляют профили:

```json
"searches": [
  {"tag": "golang", "adapter": "hh-main", "profiles": ["backend", "golang"],
   "query": {"source": "global", "text": "Golang developer", "area": ["1"]}}
],
"jobs": [
  {"tag": "daily-applications", "triggers": [{"type": "cron", "expression": "30 9 * * *", "timezone": "Europe/Moscow"}],
   "action": {"type": "application.campaign", "profiles": ["backend", "golang"],
              "routes": ["golang"], "target_successful": 40, "max_in_flight": 2}}
]
```

## Цепочка поисков с fallback

Если глобальная выдача закончилась, а цель по откликам не достигнута,
кампания переходит к следующему route.

```json
"searches": [
  {"tag": "golang-global", "adapter": "hh-main", "profiles": ["main"],
   "priority": 100, "target_applications": 20, "fallback": "golang-similar",
   "query": {"source": "global", "text": "Golang developer", "area": ["1"], "page_size": 20, "max_pages": 2}},

  {"tag": "golang-similar", "adapter": "hh-main", "profiles": ["main"],
   "priority": 50, "query": {"source": "similar_resume", "resume": "resume-id", "area": ["1"]}}
]
```

## Ежедневное поднятие и публикация резюме

```json
"jobs": [
  {"tag": "touch-resume", "enabled": true,
   "triggers": [{"type": "cron", "expression": "0 10 * * *", "timezone": "Europe/Moscow",
                 "misfire": "run_once", "jitter": {"min": "5m", "max": "30m"}}],
   "concurrency": "forbid",
   "action": {"type": "resume.touch", "profile": "main"}}
]
```

Публикация изменённого резюме — отдельный job `resume.publish`; платформенный
cooldown worker пережидает сам (`next_publish_at`).

## Автоответы на опросники в чатах

Кликом кнопок можно отвечать вручную из dashboard, а можно — известными
ответами из `answer_sets`. Нужны `allow_send` и `answer_known`:

```json
"profiles": [{
  "tag": "main",
  "adapter": "hh-main",
  "state_file": "/data/profiles/main.json",
  "conversations": {"allow_send": true, "allow_mark_read": true, "answer_known": true}
}],
"answer_sets": ["answers/conversation/invitation.json"]
```

`answers/conversation/invitation.json` — один вопрос на блок:

```json
{
  "tag": "invitation",
  "name": "Приглашение: посмотрю вакансию",
  "kind": "conversation",
  "platform": "hh",
  "match": {"topic": "Ответьте на приглашение, даже если оно вам не интересно. Так мы сможем рекомендовать вам более подходящие вакансии. Отправить ответ можно одной кнопкой:"},
  "answers": [
    {
      "question": "Ответьте на приглашение, даже если оно вам не интересно. Так мы сможем рекомендовать вам более подходящие вакансии. Отправить ответ можно одной кнопкой:",
      "selected_options": ["Посмотрю вакансию, спасибо"]
    }
  ]
}
```

Незнакомые вопросы автоматически не отправляются — их ждут кнопки в чате.
Ответы можно положить только в `data/` вне Git: там персональные данные.

## Автопрогон анкет вакансий

Когда анкета встречается повторно, reviewed-блок из `answer_sets` заполняется
автоматически. Формируйте блок по мере ответов:

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

Новый или неизвестный вопрос останавливает отклик в разделе «Проверки и
опросники»; там он заполняется одной формой, после чего отправка продолжается.

## Модель для сопроводительных

Провайдер модели + policy у профиля. Обязателен fallback-пул писем и файл
фактов резюме, чтобы модель не выдумывала опыт.

```json
"models": [
  {"tag": "openai", "type": "openai", "model": "gpt-5-mini", "api_key_env": "OPENAI_API_KEY"}
],
"profiles": [{
  "tag": "main",
  "adapter": "hh-main",
  "resume": "resume-id",
  "resume_facts_file": "facts/main.json",
  "state_file": "/data/profiles/main.json",
  "applications": {
    "mode": "submit",
    "message_template_file": "messages/backend.json",
    "model": {"provider": "openai", "prompt_version": "v1",
              "instruction": "Пиши короткое сопроводительное по фактам резюме и вакансии",
              "timeout": "30s"}
  }
}]
```

`facts/main.json`:

```json
{
  "tag": "main-facts",
  "resume_id": "resume-id",
  "digest": "sha256:<digest>",
  "facts": {"grade": "senior", "stack": ["Go", "PostgreSQL"], "years": 6}
}
```

Digest считается по каноническому JSON `{"resume_id": ..., "facts": ...}`:

```sh
python3 - <<'PY'
import hashlib, json
resume_id = "resume-id"
facts = {"grade": "senior", "stack": ["Go", "PostgreSQL"], "years": 6}
canonical = json.dumps({"resume_id": resume_id, "facts": facts},
                       ensure_ascii=False, separators=(",", ":"), sort_keys=True)
print("sha256:" + hashlib.sha256(canonical.encode()).hexdigest())
PY
```

При ошибке модели или невалидном ответе письмо берётся из fallback-пула.

## Напоминание рекрутеру

Один job выбирает подходящий диалог и планирует одноразовый follow-up.

```json
"jobs": [
  {"tag": "remind-employer", "enabled": true,
   "triggers": [{"type": "cron", "expression": "15 11 * * 1-5", "timezone": "Europe/Moscow"}],
   "concurrency": "forbid_per_profile",
   "action": {
     "type": "conversation.follow_up.select",
     "profile": "main",
     "follow_up": {
       "strategy": "oldest_unanswered",
       "minimum_silence": "72h",
       "run_after": "1m",
       "deadline_after": "24h",
       "content": {"text": "Добрый день! Актуальна ли ещё вакансия?"},
       "policy": {"cancel_on_incoming": true, "require_active_conversation": true,
                  "max_follow_ups": 1, "cooldown": "72h"}
     }
   }}
]
```

Требуется `conversations.allow_send: true`. Входящий ответ отменяет отправку.

## Очистка старых откликов

```json
"jobs": [
  {"tag": "cleanup", "enabled": true,
   "triggers": [{"type": "cron", "expression": "0 3 * * *", "timezone": "Europe/Moscow"}],
   "concurrency": "forbid_per_profile",
   "action": {"type": "application.retention", "profile": "main",
              "retention": {"stale_after": "336h", "remove_rejected": true}}}
]
```

Удаление локальное, с tombstone для dedup и защитой приглашений от удаления.

## Автообновление «О себе» из конфига

Опишите desired state как resource и поставьте job `resume.update`: worker
сделает plan → apply → read-back и, при желании, публикацию.

```json
"resources": [
  {"tag": "main-about", "type": "profile_state", "profile": "main",
   "ownership": "declared_fields",
   "state": {"resumes": {"resume-id": {"web": {"skills": ["Backend-разработчик на Go. Люблю простые решения и честные тесты."]}}}}}
],
"jobs": [
  {"tag": "update-about", "enabled": true,
   "triggers": [{"type": "cron", "expression": "0 10 * * 1", "timezone": "Europe/Moscow"}],
   "concurrency": "forbid_per_profile",
   "action": {"type": "resume.update", "profile": "main", "resource": "main-about", "publish": true}}
]
```

## Разрешить смену видимости резюме

Некоторые работодатели требуют видимость «всем». Отклик по умолчанию
останавливается с причиной `resume_visibility_change_required`; разрешить
изменение можно явно:

```json
"applications": {"mode": "submit", "allow_visibility_change": true}
```

## Ручной запуск и проверки

```sh
job-agent-check ./deploy/config.json     # ready | degraded | blocked
curl -s -H "Authorization: Bearer $JOB_AGENT_API_TOKEN" http://127.0.0.1:8080/readyz
curl -s -X POST -H "Authorization: Bearer $JOB_AGENT_API_TOKEN" \
  -H "Idempotency-Key: $(uuidgen)" \
  http://127.0.0.1:8080/api/v1/jobs/daily-applications/runs
```

## Backup и восстановление

```sh
job-agent db backup  --config ./deploy/config.json
job-agent db restore --config ./deploy/config.json --input <backup> --force
```

Выполняйте на остановленном сервисе и после backup перед обновлением.
