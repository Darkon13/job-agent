# `answer_sets`

Список файлов с готовыми ответами. Registry собирает их в runtime-блоки:
`qualification` — reviewed question bank для family/level, `conversation` —
тематический блок для опросников в чате.

## Пример

```json
"answer_sets": [
  "answers/qualification/hh-go-basics.json",
  "answers/conversation/*.json"
]
```

| Поле | Тип | Обяз. | Описание |
|---|---|---|---|
| элемент массива | string | **да** | Путь или glob-маска относительно файла, который объявил `answer_sets`. |

## Условия использования

- **`conversation`-блок** применяется только при
  `profiles[].conversations.answer_known: true` и требует `allow_send`.
  Совпадение строгое: topic/fingerprint и точный текст кнопки; неизвестный,
  неоднозначный или устаревший вариант остаётся человеку.
- **`qualification`-блок** применяется в прохождении тестов после review:
  ответы индексируются по `question_fingerprint`, а не по позиции варианта.
  Draft может не иметь fingerprint — тогда нужен review.

!!! warning "Ещё не реализовано"
    Режим `review_only`, кэш предложений модели и лимиты стоимости не
    реализованы; авто-режим квалификаций работает, но `code`-вопросы уходят
    в review и не отвечаются автоматически.
- Внешние подборки сначала импортируются в `study bank`
  (`platform: study`, `verification: external_unverified`) и не считаются
  reviewed-ответами автоматически.

## Форматы файлов

Один блок — один JSON-файл. Для квалификаций поддерживается family bundle с
секциями `levels`, который builder разворачивает в отдельные блоки по
`(platform, family, level)`. Полные форматы, fingerprint и правила ревизий
описаны в [контракте fallback-модели](../../next-answer-model-fallback.md) и
[контракте квалификаций HH](../../hh-api-contracts.md).

## Связанные документы

- [Conversation API](../../conversation-api.md) — review-сессии и отправка;
- [Отклики](applications.md) — `conversations.answer_known` и chat-ответы;
- [Model fallback](../../next-answer-model-fallback.md) — цепочка
  known-answer → model → human.
