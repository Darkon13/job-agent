# HH: browser chat contract

Наблюдения сделаны 2026-07-18 и уточнены 2026-09-07 в авторизованной applicant session. Имена,
сообщения, chat/account/vacancy/resume IDs и значения cookies не фиксируются.
Ни одного сообщения или варианта ответа во время исследования не отправлено.

## UI boundary и навигация

В обычных страницах чат открывается как cross-origin iframe:

```text
main page hh.ru
`- [data-qa="chatik-root"]
   `- iframe chatik.hh.ru?platform=...&dest=...
```

Основные controls оболочки:

| Элемент | `data-qa` |
|---|---|
| Открыть chat widget | `chatikActivator-button` |
| Root | `chatik-root` |
| Открыть в отдельной вкладке | `chatik-open-in-new-tab-button` |
| Закрыть widget | `chatik-close-chatik` |

Полноэкранный маршрут — `https://hh.ru/chat/<chat-id>`. В нём список диалогов
использует `chatik-open-chat-<chat-id>`, а ссылка связанной вакансии —
`chatik-header-vacancy-link`.

Browser transport должен явно выбирать iframe/full-page context. Селекторы
основной страницы не применяются внутри cross-origin frame автоматически.

## Переключение диалога

При открытии/переключении наблюдалась последовательность:

```text
GET  /chatik/api/filter_clusters
POST /chatik/api/notify_chat_opened
GET  /chatik/api/chats?filterUnread=false&filterHasTextMessage=false&from=...
GET  /chatik/api/chat_data?chatId=...&lastMessageId=...
POST /chatik/api/mark_read                  # при наличии unread state
GET  https://websocket.hh.ru/proxy-webapp-config
```

`chats` возвращает `chats.items` и cursor `chats.nextFrom`; элементы содержат
`currentParticipantId`, `lastMessage`, `unreadCount`, `operations` и ссылки на
resources. `chat_data` в актуальном applicant flow не требует `applicantId`, а
историю продолжает через `lastMessageId`. Оба GET-запроса сами по себе не
отмечают чат прочитанным.

Каталог упорядочен по `lastActivityTime` по убыванию и у активного аккаунта
может содержать тысячи исторических чатов (`found: 3474` на живом профиле).
Discovery поэтому читает ограниченное recent-окно (`maxChatDiscoveryPages` ×
20 = 1000 последних по активности чатов) и возвращает `Truncated: true` вместо
ошибки; новая активность всегда поднимает чат в это окно. Полный history sync
планируется только для новых чатов и для чатов, у которых изменились статус,
unread или последнее сообщение, поэтому периодический discovery не создаёт
sync-задачи для неизменённых диалогов.

`filter_clusters` принимает web-фильтры `filterUnread` и
`filterHasTextMessage`. Live updates доставляются через websocket proxy;
периодический poll остаётся fallback, а не основным способом наблюдения.

Открытие чата имеет побочный эффект: unread dialog может быть помечен
прочитанным. Поэтому read marker является отдельной платформенной командой в
audit trail, даже если текущий web UI отправляет его автоматически.

Политики `none | refusals | selected | all`, classifier отказов и idempotency
для массового чтения описаны в `hh-browser-operations.md`. Poller не должен
неявно превращать получение metadata в `mark_read` для всех диалогов.

## `chat_data`

Ответ агрегирует доменные данные и UI state:

- `chat`: ID/type, unread count, participants, operations, messages и
  `writePossibility`;
- `resources`: vacancies, resumes, participants, negotiation topics,
  `test_solutions`, `employer_assistant` и uploads;
- `chatStates`: возможность писать, отправлять файлы и response reminders;
- `suggestions`: UUID и `suggestionOptions.options`;
- `hasMessagesWithTextButtons`;
- `display`, missing resources и call state.

Message содержит text/type, participant, resources, timestamps, edit/delete
flags, workflow transition и `participantDisplay.isBot`. Bot detection должен
использовать structured markers (`isBot`, `employer_assistant`, workflow), а не
только слова «бот» или «ассистент» в отображаемом тексте.

Наблюдаемые DOM locators обычного message bubble:

| Элемент | `data-qa` |
|---|---|
| Message | `chatik-chat-message-<message-id>` |
| Message text | `chatik-chat-message-<message-id>-text` |
| Bubble | `chat-bubble-wrapper` |
| Author | `chat-bubble-author-name` |
| Time | `chat-buble-display-time` |
| Send | `chatik-do-send-message` |

Доступность textarea зависит от `chatStates.writeMessageState.allowed` и
`chat.writePossibility`. Отсутствие composer не следует трактовать как DOM bug.

## Текст, варианты и workflow events

Frontend bundles содержат отдельные endpoints:

- `/chatik/api/send` — обычное сообщение/редактирование;
- `/chatik/api/quick_replies` — доступные quick replies;
- `/chatik/api/send_event` — действие по встроенной кнопке/workflow;
- `/chatik/api/upload_file` — attachment;
- `/chatik/api/delete_message` — удаление.

Для text send подтверждён минимальный `POST /chatik/api/send` с body
`{chatId, idempotencyKey, text}`; endpoint принимает query
`hhtmSourceLabel=chat&hhtmSource=chat`. Перед POST transport повторно читает
`chat_data` и проверяет `chatStates.writeMessageState.allowed`. Идемпотентный
ключ внешнего workflow детерминированно преобразуется в UUID, поэтому retry не
создаёт новый платформенный запрос с другим identity.

Полная frontend-модель также оперирует `resources/uploadId`, optional edit/message ID и
`suggestionUuid`. Для event path встречаются `messageId`, `event`, `buttonName`,
`eventParams` и типы `chat_text_button`, `chat_event_button`,
`chat_link_button`.

В исследованных чатах активных suggestions не было: options были пусты и
`hasMessagesWithTextButtons=false`. Контракт, однако, явно разделяет свободный
текст и выбор готового варианта. Точный request body фиксируется при естественном
появлении активной кнопки; нажимать её ради исследования нельзя.

Явный `POST /chatik/api/mark_read` принимает как минимум `chatId`, `messageId`
и `hasUnreadDiscardMessage`. Transport выбирает последнее входящее видимое
сообщение из актуального `chat_data`; нулевой `unreadCount` завершается без POST.

Целевой adapter command:

```go
type ConversationAction struct {
    Kind           string // text | suggestion | workflow_event
    ChatID         string
    PromptMessageID string
    Text           string
    SuggestionUUID string
    Event          string
    ButtonName     string
    IdempotencyKey string
}
```

Для `suggestion`/`workflow_event` handler выбирает только один из вариантов,
которые присутствуют в последнем `chat_data`. Произвольный текст вместо
обязательной кнопки не отправляется.

## Tests и questionnaires

В существующих чатах обнаружены связанные вакансии с
`userTestPresent=true` и `resources.test_solutions`. Наблюдаемый completed result
содержит `uidPk`, `examined`, `score` и `mark`. На странице такого чата отдельной
формы уже нет: это metadata завершённого теста, а не его вопросы.

Public vacancy search предоставляет ранний marker `has_test`; vacancy/chat
resources дополняют его `userTestPresent`. Активный тест исследуется отдельным
browser flow при естественном появлении, без повторного прохождения уже
завершённого результата.

Questionnaire в сообщении нормализуется отдельно от vacancy test:

```text
chat_data update
-> extract prompt + allowed options
-> current-prompt fingerprint
-> known answer / operator / manual pending
-> validate option still active
-> send_event with idempotency key
-> wait for next workflow state
```

## Activity policy

Допустима автоматическая реакция на новый recruiter/bot prompt, включая выбор
готового варианта. Повторная отправка сообщений без нового prompt только ради
искусственного поднятия активности не входит в adapter: она создаёт spam,
нарушает idempotency и может противоречить правилам платформы.

Минимальные guards:

- одна command на `(chat, prompt_message, workflow_state)`;
- перед отправкой повторно загрузить `chat_data`;
- per-chat cooldown и дневной лимит;
- никогда не отвечать на собственное последнее сообщение;
- отключать automation при неизвестном workflow или human takeover;
- сохранять audit event без полного текста персональной переписки.
