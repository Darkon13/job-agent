# Model fallback для вопросов тестов и опросников

Статус: контракт зафиксирован, реализация не начата. Документ владеет
конкретным срезом «неизвестный вопрос → model → ручное ревью». Общий контракт
processor-ов и provider-neutral model port описан в
[`docs/profile-desired-state-and-routing.md`](profile-desired-state-and-routing.md);
application-срез model operator уже реализован и описан в
[`docs/next-cover-letter-routing.md`](next-cover-letter-routing.md).

## Зачем

Сейчас неизвестный вопрос завершает qualification-попытку досрочно или создаёт
review session, и пользователь отвечает вручную. Это правильно как граница
безопасности, но медленно: один и тот же вопрос может встречаться в разных
профилях и попытках.

Целевая цепочка для каждого вопроса:

```text
known-answer (reviewed AnswerBlock, точный fingerprint)
-> model operator + обязательный локальный validator
-> manual / pending review
```

Model никогда не заменяет known-answer и не повышает verification сам. Платформа
или человек остаётся арбитром правильности: успешный `QualificationResult` или
явное подтверждение человека делает ответ reusable, как и раньше.

## Границы

- `code` вопросы не отправляются модели: они остаются `unsupported`.
- Contextual вопросы (зарплата, доступность, переезд, личные условия) не
  отправляются модели. Их источник — profile policy или manual review.
- Модель не получает переписку, credentials, полное резюме, vacancy description
  и другие данные профиля. Контекст ограничен самим вопросом и его вариантами.
- Модель не создаёт новый `AnswerBlock`, не меняет существующий и не помечает
  ответ verified.
- Семантическая догадка при submit запрещена: отправляется только ответ,
  прошедший строгий локальный validator; иначе вопрос уходит в manual review.

## Входные данные

Структурированный и ограниченный запрос:

```json
{
  "platform": "hh",
  "question_kind": "single",
  "question_text": "Какой оператор выбирает строки, у которых есть пара?",
  "options": ["INNER JOIN", "LEFT JOIN"],
  "prompt_version": "answer-fallback-v1"
}
```

- `question_text` — точный показанный текст; нормализация применяется только для
  сопоставления, не для отправки.
- `options` — все показанные варианты с исходным текстом. Пустой список для
  открытых ответов.
- `qualification_family/level` могут передаваться как метаданные для provenance,
  но не как подсказка «какие ответы ожидаются».
- Другие вопросы, ответы пользователя и история попытки в контекст не входят.

## Выходные данные

```json
{
  "selected_options": ["INNER JOIN"],
  "text": "",
  "confidence": "high",
  "model": "gpt-...",
  "response_id": "..."
}
```

`rationale`/reasoning модели не сохраняются в базе и не попадают в AnswerBlock;
human review может показать краткое объяснение только в UI, если провайдер его
вернул, но это не является частью контракта.

## Обязательный локальный validator

Проверка не зависит от provider schema:

- `single`: ровно один вариант, точное нормализованное совпадение с показанным
  вариантом, `text` пуст;
- `multiple`: непустое подмножество показанных вариантов, без дублей;
- `text`: непустой ограниченный текст, без placeholders, JSON/code fence,
  управляющих символов и контактов; вопрос не помечен contextual;
- для `single`/`multiple` модель обязана выбирать только из переданного списка;
  собственный текст варианта не создаётся;
- любая ошибка классифицируется как `invalid_output` и не покидает operator
  chain: вопрос переходит к manual/pending review, попытка не отправляется.

## Provenance

Вместе с выбором сохраняется versioned provenance без персональных данных:

- resolver: `known_answer` | `model` | `human` | `study_bank`;
- model tag и provider model;
- `prompt_version`, `response_id`, input/output digests;
- confidence и безопасная категория fallback.

Промпт, полный provider error и reasoning не сохраняются. Ответ модели
записывается как `unverified` до подтверждения платформой или человеком;
повторное использование регулируется существующей монотонной policy
`QualificationResult`.

## Политика по умолчанию

- Без настроенного `models`-провайдера и policy поведение не меняется:
  unknown question → manual review.
- При настроенной policy модель вызывается один раз на вопрос; timeout, rate
  limit, отмена или invalid output не блокируют попытку, а переводят вопрос в
  manual review.
- `auto_submit` включается только явной policy. Для qualification он означает,
  что валидный ответ модели отправляется платформе, а успешный результат
  становится подтверждением, как для human-ответа. Для `review_only` модель
  только предлагает вариант, который человек принимает или отклоняет.
- Model suggestions для одного и того же `question_fingerprint` кэшируются по
  fingerprint + prompt version + provider model, чтобы не платить за повторный
  вызов; кэш не считается ответом и не используется при submit без validator.

## Порядок реализации

1. Зафиксировать контракт; решение по `auto_submit` для qualification принимает
   пользователь.
2. Типы `AnswerModelRequest/Response`, локальный validator и тесты с fake
   generator; переиспользовать `ModelFailureKind` и timeout/error taxonomy.
3. Config policy на уровне профиля/answer routing: provider tag, prompt version,
   timeout, `auto_submit`/`review_only`.
4. Хранение suggestion (`review_only`) рядом с review prompt и API/dashboard
   рендер; в `auto` сохраняется только provenance.
5. Встроить цепочку в создание review session и qualification runner.
6. Live-проверка на реальном неизвестном вопросе: exact option match, read-back,
   запись provenance и последующая попытка из подтверждённого блока.

## Открытые решения

- Нужен ли `study_bank` как отдельный источник suggestion для human review, без
  права submit.
- Лимиты стоимости: максимум вызовов на попытку и дневной бюджет.
