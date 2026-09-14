# API чатов и автоматизация

Conversation API — единая точка управления чатами на платформах и опциональным
адаптером пользовательского Telegram-аккаунта для переписки с рекрутерами. UI,
MCP и клиенты уведомлений используют этот API вместо прямого обращения к
адаптеру или браузеру.

## Ресурсы

```text
GET    /api/v1/profiles/{profile_id}/conversations
GET    /api/v1/conversations/{conversation_id}
POST   /api/v1/conversations/{conversation_id}/sync
GET    /api/v1/conversations/{conversation_id}/messages?cursor=...
POST   /api/v1/conversations/{conversation_id}/messages
POST   /api/v1/conversations/{conversation_id}/mark-read
POST   /api/v1/conversations/mark-read

GET    /api/v1/conversations/{conversation_id}/follow-ups
POST   /api/v1/conversations/{conversation_id}/follow-ups
GET    /api/v1/follow-ups/{follow_up_id}
PATCH  /api/v1/follow-ups/{follow_up_id}
DELETE /api/v1/follow-ups/{follow_up_id}
POST   /api/v1/follow-ups/{follow_up_id}/run
```

Текущие list-handlers возвращают упорядоченный массив `items`. До включения
неограниченной production-истории необходимо добавить cursor pagination на
уровне storage. Ответы с диалогами и сообщениями содержат нормализованные поля;
сырые payload платформы и данные авторизации наружу не выдаются. `sync`, отправка
сообщения, одиночный и bulk `mark-read`, а также `run` являются асинхронными
командами и возвращают
`202 Accepted` с ID durable task. Для команд, создания и удаления требуется
заголовок `Idempotency-Key`; revision-safe `PATCH` дополнительно использует
`If-Match`.

На первом этапе сервер слушает только loopback-адрес. Публичный bind запрещён до
реализации аутентификации и авторизации API.

Немедленная отправка сообщения принимает ровно один источник содержимого:

```json
{
  "reply_to_id": "message-42",
  "content": {
    "operator": "employer-reply"
  }
}
```

Альтернативы: `content.text` для явно заданного пользователем текста и
`content.template` для настроенного шаблона. Сам оператор может содержать
fallback-цепочку model/template, но API-запрос не может передавать несколько
конкурирующих источников.

## Одноразовые follow-up таймеры

Конкретное напоминание — persistent one-shot timer, а не повторяющаяся cron
задача:

```json
{
  "anchor_message_id": "message-42",
  "anchor_at": "2026-07-19T12:00:00+03:00",
  "run_at": "2026-07-22T12:00:00+03:00",
  "deadline": "2026-07-22T18:00:00+03:00",
  "content": {
    "template": "remind-employer"
  },
  "policy": {
    "cancel_on_incoming": true,
    "require_active_conversation": true,
    "max_follow_ups": 1,
    "cooldown": "72h"
  }
}
```

При создании follow-up сохраняется как durable timer и источник истины.
Scheduler выбирает due-таймеры в статусе `scheduled` и идемпотентно ставит в
очередь задачу `conversation.follow_up`. `run_at` становится
`Task.AvailableAt`, а `deadline` ограничивает запоздалое выполнение после
простоя. Такая схема сохраняет корректность `PATCH` и ручного запуска до
enqueue. Падение рядом с enqueue исправляется следующим reconcile. После claim
worker повторно загружает follow-up и диалог непосредственно перед отправкой.

Follow-up отменяется, если:

- после anchor пришло новое входящее сообщение;
- диалог закрыт, отклонён или архивирован;
- связанный отклик перешёл в terminal state;
- таймер заменён более новым напоминанием;
- пользователь удалил его или execution policy запретила отправку.

`PATCH` изменяет только follow-up в статусе `scheduled` и требует `If-Match` с
его revision. `DELETE` выполняет идемпотентную отмену и не удаляет audit history.
`POST .../run` делает таймер due немедленно, но не отключает защитные проверки.
Лимит и cooldown проверяются по сохранённой истории отправок, поэтому рестарт
процесса их не сбрасывает.

## Автоматизация через cron и события

Cron используется для периодической проверки политик. Он должен создавать или
сверять ограниченные одноразовые follow-up таймеры, а не отправлять сообщения
напрямую:

```json
{
  "tag": "remind-oldest-employer",
  "enabled": true,
  "triggers": [
    {
      "type": "cron",
      "expression": "15 11 * * 1-5",
      "timezone": "Europe/Moscow",
      "misfire": "run_once"
    }
  ],
  "concurrency": "forbid",
  "action": {
    "type": "conversation.follow_up.select",
    "profile": "primary",
    "follow_up": {
      "strategy": "oldest_unanswered",
      "minimum_silence": "72h",
      "run_after": "1m",
      "deadline_after": "24h",
      "content": {
        "text": "Добрый день! Подскажите, пожалуйста, актуальна ли ещё вакансия?"
      },
      "policy": {
        "cancel_on_incoming": true,
        "require_active_conversation": true,
        "max_follow_ups": 1,
        "cooldown": "72h"
      }
    }
  }
}
```

`limit` ограничивает число выбранных чатов (по умолчанию 1), `all: true` берёт все подходящие чаты (в пределах 50 за запуск).

Стратегии выбора: `oldest_unanswered`, `newest_unanswered`, `random` и
`longest_silence`. Первые три ждут ответа работодателя (последнее сообщение —
наше), `longest_silence` смотрит только на длительность тишины: подходит любой
активный диалог, где последнее сообщение (в любую сторону, включая «ожидайте»)
старше `minimum_silence`, и выбирает самый давний.
Последняя использует стабильный hash `(profile, task idempotency key)`, поэтому
retry не меняет уже сделанный выбор. Перед выбором исключаются диалоги без
исходящего сообщения, чаты с более новым входящим ответом, неактивные диалоги,
conversation с pending follow-up, нарушенный minimum silence/cooldown и уже
исчерпанный `max_follow_ups`. Если подходящего диалога нет, job корректно
завершается без создания сообщения.

Выбор создаёт обычный durable one-shot follow-up и не отправляет сообщение
напрямую. Повтор того же scheduled task сначала находит follow-up по общей
idempotency boundary, поэтому после частичного сбоя не выбирается второй чат.
HH browser transport поддерживает каталог, историю, отправку текста и
`mark-read`. Чтение включается наличием валидного browser state. Мутации
разрешаются независимо через `profiles[].conversations.allow_send` и
`allow_mark_read`; без флага transport возвращает `unsupported` до внешнего
POST. Follow-up job дополнительно не регистрируется без реально привязанного
transport-а.

Плановая синхронизация описывается отдельной read-only job:

```json
{
  "tag": "sync-primary-conversations",
  "enabled": true,
  "triggers": [{
    "type": "cron",
    "expression": "*/10 * * * *",
    "timezone": "Europe/Moscow",
    "misfire": "run_once"
  }],
  "concurrency": "forbid",
  "action": {"type": "conversation.sync", "profile": "primary"}
}
```

Её первый task имеет тип `conversation.discover`; он не открывает чат в UI и
не вызывает `mark_read`. Для каждого найденного диалога создаётся отдельный
идемпотентный `conversation.sync`, поэтому частичный сбой не требует повторно
скачивать уже сохранённые сообщения.

Event trigger может запланировать ту же policy после исходящего сообщения или
изменения состояния отклика. Входящие сообщения и terminal events отклика
отменяют подходящие ожидающие таймеры. Scheduler, REST endpoint и event handler
сходятся к одной сущности follow-up и общей idempotency boundary.

## События

Conversation pipeline публикует нормализованные события:

- `conversation.message_received`;
- `conversation.message_queued`;
- `conversation.message_sent`;
- `conversation.message_failed`;
- `conversation.marked_read`;
- `conversation.follow_up_scheduled`;
- `conversation.follow_up_cancelled`.

Payload события содержит ID и структурированные метаданные, но не полную
приватную историю переписки. Полный текст остаётся в conversation storage и
удаляется согласно настроенной retention policy.
