# Conversation API and automation

Conversation API is the common control plane for platform chats and an optional
Telegram recruiter-account adapter. UI, MCP and notification clients use this
API instead of calling an adapter or browser directly.

## Resources

```text
GET    /api/v1/profiles/{profile_id}/conversations
GET    /api/v1/conversations/{conversation_id}
POST   /api/v1/conversations/{conversation_id}/sync
GET    /api/v1/conversations/{conversation_id}/messages?cursor=...
POST   /api/v1/conversations/{conversation_id}/messages
POST   /api/v1/conversations/{conversation_id}/mark-read

GET    /api/v1/conversations/{conversation_id}/follow-ups
POST   /api/v1/conversations/{conversation_id}/follow-ups
GET    /api/v1/follow-ups/{follow_up_id}
PATCH  /api/v1/follow-ups/{follow_up_id}
DELETE /api/v1/follow-ups/{follow_up_id}
POST   /api/v1/follow-ups/{follow_up_id}/run
```

List methods use cursor pagination. Conversation and message responses contain
normalized fields; raw platform payloads and authentication data are not
exposed. `sync`, `messages`, `mark-read` and `run` are asynchronous commands and
return `202 Accepted` with a durable task ID. Every write command requires an
`Idempotency-Key` header.

An immediate message accepts exactly one content source:

```json
{
  "reply_to_id": "message-42",
  "content": {
    "operator": "employer-reply"
  }
}
```

The alternatives are `content.text` for explicit user text and
`content.template` for a configured template. An operator may itself contain a
model/template fallback chain, but an API request cannot provide several
competing sources.

## One-shot follow-ups

A concrete reminder is a persistent one-shot timer, not a repeating cron task:

```json
{
  "anchor_message_id": "message-42",
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

Creation stores the follow-up and its delayed `conversation.follow_up` task in
one transaction. `run_at` maps to `Task.AvailableAt`; `deadline` bounds late
execution after downtime. The worker claims the task and then reloads both the
follow-up and conversation immediately before sending.

The follow-up is cancelled when:

- an incoming message is newer than its anchor;
- the conversation is closed, rejected or archived;
- the related application becomes terminal;
- a newer reminder supersedes it;
- the user deletes it or an execution policy denies it.

`PATCH` only changes a `scheduled` follow-up and requires `If-Match` with its
revision. `DELETE` is an idempotent cancellation; it does not delete audit
history. `POST .../run` makes the timer due now but still executes every guard.
The limits and cooldown are checked against sent-message history, so process
restarts cannot reset them.

## Cron and event automation

Cron is useful for recurring policy evaluation. It must create or reconcile
bounded one-shot follow-ups rather than send messages directly:

```json
{
  "tag": "reconcile-employer-follow-ups",
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
    "type": "conversation.follow_up.reconcile",
    "profile": "primary",
    "policy": "employer-reminder"
  }
}
```

An event trigger can schedule the same policy after an outgoing message or
application state change. Incoming messages and terminal application events
cancel matching pending timers. The scheduler, REST endpoint and event handler
all converge on the same follow-up entity and idempotency boundary.

## Events

The conversation pipeline emits normalized events:

- `conversation.message_received`;
- `conversation.message_queued`;
- `conversation.message_sent`;
- `conversation.message_failed`;
- `conversation.marked_read`;
- `conversation.follow_up_scheduled`;
- `conversation.follow_up_cancelled`.

Event payloads contain IDs and structured metadata, not full private message
history. Full text remains in conversation storage with the configured
retention policy.
