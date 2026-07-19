# HH: applicant browser operations

Наблюдения сделаны 2026-07-18 в авторизованной applicant session. Значения
cookies, XSRF, идентификаторы аккаунта/резюме/чатов и персональные данные не
фиксируются.

Документ дополняет API-контракты browser-only и hybrid поведением. DOM и
внутренние web endpoints не являются публичным API: перед реализацией и после
изменений HH их нужно проверять integration tests.

## Capability inventory

| Capability | Предпочтительный transport | Примечание |
|---|---|---|
| `vacancies.inspect` | API, затем browser | Полное чтение карточки до решения об отклике |
| `applications.inspect_requirements` | browser | Определение письма, test/questionnaire до submit |
| `applications.submit` | API или browser | Browser нужен для обязательной web-формы |
| `conversations.mark_read` | browser/chatik | Открытие чата само может отправить `mark_read` |
| `resumes.publish` | API | Планировать по `next_publish_at` |
| `resumes.read/create/update` | API profile schema | Browser остаётся fallback и discovery tool |
| `skills.list_methods` | browser | Просмотр доступных проверок навыков |
| `skills.start_attempt` | browser | Отдельное подтверждаемое действие |
| `activity.observe` | browser | Только telemetry, не механизм накрутки |

## Vacancy discovery и раннее обнаружение теста

Search page содержит JSON в:

```text
template#HH-Lux-InitialState
`- vacancySearchResult.vacancies[]
   |- vacancyId
   |- userTestPresent
   `- userTestId
```

Это позволяет отфильтровать вакансии с тестом до отклика. На карточке те же
признаки встречаются в `vacancyInternalInfo` и
`applicantVacancyResponseStatuses.<vacancy-id>.shortVacancy`.

Adapter должен извлекать их как hints, а не считать неизменяемой схемой:

```go
type ApplicationRequirements struct {
    CoverLetterRequired bool
    HasQuestionnaire    bool
    QuestionnaireID     string
    Required            bool
}
```

`userTestPresent=false` не гарантирует, что после клика не появится новая
валидация. Финальный authority — response popup/form непосредственно перед
submit.

## Обычный web-отклик

Для вакансии без теста наблюдалась последовательность:

```text
GET  /applicant/vacancy_response/popup?...vacancyId=...
POST /applicant/vacancy_response/popup
```

POST использовал multipart поля:

- `resume_hash`;
- `vacancy_id`;
- `letterRequired`;
- `lux`;
- `ignore_postponed`;
- `mark_applicant_visible_in_vacancy_country`;
- `country_ids`.

Успешный ответ содержал `success`, negotiation/chat identifiers,
`applicantActivity`, `responseStatus.test.hasTests`, состояния резюме,
`responseImpossible` и `alreadyApplied`.

До POST adapter обязан повторно проверить уникальность
`(profile_id, vacancy_id)`, требования формы и выбранное резюме. Идентификаторы
topic/chat сохраняются как platform references, но не выводятся в обычные логи.

## Vacancy questionnaire/test form

При наличии обязательного теста кнопка отклика открывает:

```text
GET /applicant/vacancy_response?vacancyId=...&startedWithQuestion=false
```

Initial state содержит:

```text
vacancyResponsePopup.vacancy.test
|- hasTests
|- testId
`- required

vacancyTests.<vacancy-id>
|- uidPk
|- guid
|- description
|- required
|- startTime
`- tasks[]
   |- id
   |- description
   |- multiple
   |- open
   `- candidateSolutions[]
```

Наблюдаемый open-text task был представлен `textarea` с именем
`task_<task-id>_text`. Форма отправляется POST на текущий
`/applicant/vacancy_response?...` как
`application/x-www-form-urlencoded` и содержит:

- `_xsrf`;
- `uidPk`;
- `guid`;
- `startTime`;
- `testRequired`;
- `task_<task-id>_text` для открытого ответа;
- аналогичные task-specific поля для других типов, которые нужно исследовать
  отдельно.

Submit button: `vacancy-response-submit-popup`.

Extractor не хранит XSRF/guid в `TestDefinition`. Они являются short-lived
submission context внутри зашифрованного profile state. Содержание теста
нормализуется и отправляется оператору, но фактический submit выполняется только
после валидации обязательных полей и выбранной execution/approval policy.

Portable qualification `AnswerBlock` хранит tag/name, platform, family/level,
`question_fingerprint`, текст вопроса и текст выбранного варианта, но не
task/option ID. Browser раскрывает вопросы последовательно; core вычисляет
fingerprint текущего вопроса и сразу строит ответ с его runtime IDs. Блок
пополняется по мере прохождений. Итоговый fingerprint наблюдавшейся попытки
нужен только для аудита и не используется как предварительный matcher. Блок
заполняется или импортируется пользователем; browser discovery не добывает и не
помечает ответы как правильные.

## Чтение сообщений и отказов

Открытие unread-чата может автоматически вызвать
`POST /chatik/api/mark_read`. Поэтому чтение содержимого и изменение read-state
нужно разделять в доменной модели, даже если текущий UI объединяет их.

Предлагаемая политика:

```json
{
  "tag": "hh-read-policy",
  "mode": "refusals",
  "mark_read": true,
  "include_unknown": false
}
```

`mode`:

- `none` — только poll metadata, read-state не менять;
- `refusals` — распознать структурированный отказ и пометить его прочитанным;
- `selected` — только перечисленные conversation/event classes;
- `all` — прочитать все доступные сообщения и явно audit каждое изменение.

Classifier отказа в приоритете использует negotiation state, message/workflow
type и platform resources; текстовая эвристика является fallback. Команда
идемпотентна по `(chat_id, last_read_message_id)`.

## Активность и просмотр вакансий

На главной странице HH показывает `activity-card`, `activity-score` и progress.
Наблюдаемый help dialog перечислял сигналы активности: просмотр вакансии,
звонок/контакт и отклик. Значения и веса являются UI telemetry и могут меняться.

`activity.observe` может собирать текущий score и объяснения для UI. Отдельный
workflow искусственного повышения score через пустые открытия, ожидание и
закрытие вакансий не создаётся. `vacancies.inspect` открывает страницу только
как реальную часть qualification pipeline: получить описание, требования,
работодателя, тест и принять решение об отклике.

Такое разделение пригодится, если HH введёт фильтр откликов без фактического
просмотра: audit trail подтвердит обычный inspect перед submit без имитации
пользовательской активности.

## Публикация/поднятие резюме

Основной контракт — публичный API:

```text
GET  /resumes/mine
POST /resumes/{resume_id}/publish -> 204
```

Первая публикация публикует резюме, последующие обновляют дату. Scheduler
использует `can_publish_or_update` и `next_publish_at`; период нельзя жёстко
зашивать как четыре часа. При живом исследовании UI показывал четырёхчасовой
интервал, а web state также содержал `update_timeout`, но API timestamp остаётся
источником истины.

Нормализация ошибок:

- `400` — публикация сейчас невозможна/невалидное состояние;
- `403` — неправильная роль или авторизация;
- `404` — резюме недоступно;
- `429` — слишком рано, перепланировать после server/API timestamp.

Paid auto-raise и ручное API publish — разные capabilities. Нажимать upsell
`resume-update-button_actions` вместо доступной публикации нельзя.

## Skill verification

Каталог открывается на `/applicant/skill_verifications/methods`. Наблюдаемые
selectors:

- `skills-verification-method-container`;
- `verification-method-title`;
- `/applicant/skills/<skill-id>/verification_methods`;
- `applicant-keyskills-verification-methods-level-tab`;
- `applicant-keyskills-verification-methods-kind-card-theory`;
- `applicant-keyskills-verification-methods-kind-card-practice`;
- `applicant-keyskills-verification-methods-start-theory`.

Уровень и вид проверки выбираются до создания попытки. Нажатие start может
создать ограниченную/таймированную попытку, поэтому `skills.start_attempt` и
`skills.submit_answer` являются отдельными командами с audit и явной policy.
Discovery не запускает попытку. Английские audio assessments требуют отдельной
media/transcription capability; отсутствие её поддержки переводит задачу в
manual pending.

### Live assessment UI

После ручного запуска проверка открылась на отдельном origin:

```text
https://assessment.hh.ru/tests/<attempt-id>
```

Наблюдаемый экран single-choice:

| Назначение | Контракт |
|---|---|
| Вопрос | основной `paragraph` над списком вариантов |
| Реальный control | `input[type="radio"][name="answer"]` |
| Текст/значение | `input.value`; визуальный текст находится в соседнем `code`/`paragraph` |
| Click target | охватывающий input элемент `label`/card |
| Выбранность | DOM property `checked` |
| Следующий вопрос | `button[data-qa="footer-next-button"]`, `type="submit"` |
| Досрочно закончить | `button[data-qa="header-finish-button"]` |
| Progress | `[role="progressbar"][data-qa="progress"]` |

В DOM присутствуют два radio на визуальный вариант: внешний рабочий
`input[name="answer"]` и внутренний presentation-only Magritte radio без
`name/value`. Extractor обязан брать только controls с `name="answer"`, иначе
получит дубли.

Экран также показывает текстовый счётчик вида `1 из 10` и countdown «Всего
осталось». Серверное оставшееся время запрашивается:

```text
GET /shards/contest/get_time_left
-> {"timeLeftSeconds": <integer>}
```

Telemetry отправляется через:

```text
POST /shards/contest/report_data
```

Наблюдаемый payload содержит `taskId`, timestamp, numeric event `type` и иногда
`payload`. Type `10` отправлялся примерно каждые 20 секунд с `[0]`/`[1]`, а при
изменении UI наблюдался другой event type. Семантику числовых типов нельзя
угадывать без дополнительного подтверждения; adapter не должен воспроизводить
telemetry вручную.

DOM selection не равен подтверждённому ответу: переход на следующий вопрос
происходит только после submit `footer-next-button`. Для extractor доступны
read-only операции:

```text
question text
-> input[name=answer][] { value, checked }
-> progress/countdown snapshot
-> answer-neutral QuestionnaireCatalog
```

После ручного нажатия `footer-next-button` наблюдалась последовательность:

```text
GET  /shards/contest/get_time_left
GET  /shards/contest/get_contest_tasks
POST /shards/cert_tests/submit_user_answer
GET  /shards/cert_tests/get_current_task
```

Submit payload:

```json
{
  "taskId": "<task-id>",
  "userAnswerUuids": ["<selected-answer-uuid>"]
}
```

Числовой `taskId` относится к текущему вопросу, а выбранные ответы передаются
как UUID, не как отображаемый текст или индекс. Успех возвращал
`{"status":"ACCEPTED"}`. Следующий вопрос загружается отдельным
`get_current_task`; URL страницы при этом не меняется.

На новом вопросе все рабочие radio имели `checked=false`, а кнопка «Дальше» —
`disabled=true`. Выбор radio активирует кнопку. Progress изменился с 10 до 20,
что согласуется со счётчиком `1 из 10` → `2 из 10`, но adapter должен читать
фактические `aria-valuenow/max` и текстовый счётчик, не вычислять их сам.

Содержимое реальных квалификационных заданий и выбранность не сохраняются в
репозиторий проекта. Пользователь может передать собственный export в локальный
answer-block storage; логи содержат только fingerprint, количество вопросов и
результат строгого matching.
