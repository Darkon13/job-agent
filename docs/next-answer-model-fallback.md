# Model fallback для вопросов тестов и опросников

Статус: контракт зафиксирован; для qualification реализован режим `auto`
(`answers.model` в профиле). Ручной `review_only` пока не реализован: без
настроенной policy поведение прежнее. Документ владеет конкретным срезом
«неизвестный вопрос → model → ручное ревью». Общий контракт processor-ов и
provider-neutral model port описан в
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
- Режим `auto` включается policy `answers.model` профиля: валидный ответ
  отправляется платформе, а успешный `QualificationResult` помечает его
  `verified` и дописывает в qualification-блок как reusable revision с
  provenance. Режим `review_only` (модель только предлагает вариант человеку)
  остаётся необязательным расширением и в v1 не реализован.
- Кэш model suggestions по fingerprint + prompt version + provider model в v1 не
  реализован: вызов выполняется заново для каждой неизвестной попытки.

## Порядок реализации

1. ✅ Контракт зафиксирован; выбран режим `auto` для qualification.
2. ✅ Типы `AnswerModelRequest/Response`, локальный validator, provenance и
   тесты с fake generator; переиспользованы `ModelFailureKind` и timeout/error
   taxonomy.
3. ✅ Config policy `answers.model` профиля: provider tag, prompt version,
   instruction, timeout.
4. ✅ Хранение: provenance входит в `StoredAnswer`, verified-ответы
   дописываются revision в qualification-блок; отдельного suggestion-хранилища
   для `review_only` нет.
5. ✅ Цепочка встроена в qualification runner: known-answer → model → review;
   без declarative-блока используется conventional reviewed tag.
6. ⏳ Live-проверка на реальном неизвестном вопросе: exact option match,
   read-back, запись provenance и последующая попытка из подтверждённого блока.
7. ⏳ Осталось: интеграция vacancy questionnaire, `review_only`, кэш и лимиты
   стоимости, `study_bank`.

## Открытые решения

- Нужен ли `study_bank` как отдельный источник suggestion для human review, без
  права submit.
- Лимиты стоимости: максимум вызовов на попытку и дневной бюджет.
