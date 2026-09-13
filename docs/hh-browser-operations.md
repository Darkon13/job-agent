# HH: applicant browser operations

Наблюдения сделаны 2026-07-18 и повторно проверены 2026-09-06 в авторизованной
applicant session. Значения
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
| `resumes.touch` | Browser web endpoint | Планировать по `nextTouchAt` |
| `resumes.publish` | API/profile workflow | Не смешивать с периодическим поднятием |
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

## Состояния откликов (read-only)

Страница `GET /applicant/negotiations?status=all&page=N` рендерит список
откликов той же браузерной сессией, что и остальные web-операции. Данные лежат
в `HH-Lux-InitialState`: `applicantNegotiations.topicList[]` (20 на страницу) и
`applicantNegotiations.pageCount`. Полезные поля топика:

| Поле | Смысл |
| --- | --- |
| `id` | negotiation/topic ID |
| `vacancyId` | ID вакансии |
| `lastState` | `RESPONSE`, `INVITATION`, `INTERVIEW`, `DISCARD`, `HIDDEN` |
| `viewedByOpponent` | работодатель открыл отклик |
| `lastModified` | время последнего изменения (RFC3339) |

Observer переводит `lastState` в core disposition: `RESPONSE` → `pending`,
`INVITATION`/`INTERVIEW` → `invited`, `DISCARD` → `rejected`, `HIDDEN` →
`hidden`, неизвестное значение → `unknown`. `INTERVIEW` защищается от
автоматической очистки так же, как приглашение.

Страница только читается: `application.state.sync` обновляет локальные карточки,
но не отвечает, не скрывает и не удаляет отклики.

## Обычный web-отклик

Для вакансии без теста наблюдалась последовательность:

```text
GET  /applicant/vacancy_response/popup?...vacancyId=...
POST /applicant/vacancy_response/popup
```

Актуальный GET-ответ (проверен 2026-09-08) оборачивает данные формы в
`body.responseStatus`; более ранняя форма отдавала `responseStatus` на верхнем
уровне. Adapter нормализует обе формы до одного внутреннего контракта. Простое
успешное декодирование внешней оболочки недостаточно: пустой верхнеуровневый
`responseStatus` нельзя трактовать как отсутствие подходящих резюме, если
присутствует `body.responseStatus`.

POST использовал multipart поля:

- `resume_hash`;
- `vacancy_id`;
- `letter`;
- `lux`;
- `ignore_postponed`;
- `incomplete`;
- `withoutTest`;
- `mark_applicant_visible_in_vacancy_country`;
- `country_ids`.

Успешный ответ содержал `success`, negotiation/chat identifiers,
`applicantActivity`, `responseStatus.test.hasTests`, состояния резюме,
`responseImpossible` и `alreadyApplied`.

После подтверждённого отклика последующий preflight может вернуть
`type: "alreadyApplied"` без отдельного negotiation ID. Это достаточный признак
для reconciliation, но не повод повторять POST.

До POST adapter повторно выполняет popup preflight, проверяет уникальность
`(profile_id, vacancy_id)` и требования формы. Если настроенное резюме недоступно
для конкретной вакансии, workflow детерминированно выбирает первое из разрешённых
HH резюме; ручное вмешательство требуется только когда платформа не предлагает
ни одного доступного резюме. Идентификаторы
topic/chat сохраняются как platform references, но не выводятся в обычные логи.
Потерянный ответ считается неоднозначным исходом и сверяется новым GET вместо
повторного POST. CAPTCHA, тест и неизвестный flow останавливаются на ручной
проверке. Изменение видимости резюме разрешается только явной настройкой
профиля `allow_visibility_change`.

## Vacancy questionnaire/test form

Popup отклика и форма вопросов открываются одним URL; `startedWithQuestion`
управляет отображением теста:

```text
GET /applicant/vacancy_response?vacancyId=...&startedWithQuestion=false
GET /applicant/vacancy_response?vacancyId=...&startedWithQuestion=true
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

Наблюдаемые типы task и их поля формы:

| Task в state | Поле в форме |
| --- | --- |
| `open=true`, без вариантов | `textarea` `task_<task-id>_text` |
| `multiple=false`, `open=false` | `radio` `name=task_<task-id>`, `value=<solution-id>` |
| `multiple=true`, `open=false` | `checkbox` `name=task_<task-id>`, `value=<solution-id>`, несколько значений с одним именем |
| `multiple=false`, `open=true` | `radio` по вариантам плюс radio `value=open` и `textarea` `task_<task-id>_text` для своего ответа |

Ответ отправляется не как обычная форма, а тем же popup-запросом, что и
отклик без теста: `POST /applicant/vacancy_response/popup` с
`multipart/form-data`. Кроме task-полей из таблицы выше запрос содержит
контекст формы (`_xsrf`, `uidPk`, `guid`, `startTime`, `testRequired`) и
обычные поля отклика: `vacancy_id`, `resume_hash`, `ignore_postponed`,
`incomplete`, `mark_applicant_visible_in_vacancy_country`, `country_ids`,
`letter`, `lux`, `withoutTest=no`, `hhtmFromLabel`, `hhtmSourceLabel`.
Прямой urlencoded POST на страницу `vacancy_response` HH отклоняет с `400`.

Submit button: `vacancy-response-submit-popup`.

Popup с `startedWithQuestion=false` не содержит `vacancyTests` и служит только
для чтения состояния отклика; форма с вопросами рендерится при
`startedWithQuestion=true`. Если вакансия уже отработана, состояние возвращает
`vacancyResponsePopup.type=alreadyApplied`, и форма не показывается.

Живая проверка (2026-09-12, профиль `primary`, browser read session):

- 136921562 — open-text и choice с `open=true`;
- 136407992 — одиннадцать radio-вопросов и один checkbox-вопрос;
- 136408820 — только open-text.

В live-состоянии `required`, `multiple` и `open` приходят строками
(`"true"`/`"false"`), а не JSON-булевыми. Capture рендерит форму тем же URL с
`startedWithQuestion=true` и ничего не отправляет; submit заново читает форму,
заполняет все task-поля и делает POST. Choice-вопрос отправляется одним
значением `task_<id>=<solution-id>` на каждый выбранный вариант, а вопрос с
`open=true` и списком вариантов — веткой `task_<id>=open` вместе с
`task_<id>_text`. Code-вопрос попадает в каталог, но browser submitter его не
отправляет.

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

На главной странице HH ранее показывал `activity-card`, `activity-score` и
progress. Наблюдаемый help dialog перечислял сигналы активности: просмотр
вакансии, звонок/контакт и отклик. Значения и веса являются UI telemetry и могут
меняться.

При повторной проверке 2026-09-07 авторизованный `GET /applicant/profile/me`
вернул `200`, но `activity-card` и `activity-score` уже отсутствовали в HTML, а
initial state содержал включённый эксперимент
`experiments.enabled.web_hide_user_activity = "experiment"`. Это подтверждает
скрытие UI feature-флагом, но не доказывает прекращение серверного расчёта. Сам
числовой score в текущем payload не обнаружен.

Тот же initial state по-прежнему отдаёт полезные read-only метрики в
`applicantResumesStatistics`: окно `periodDays`, `searchShows`, `views`,
`invitations`, включая новые просмотры/приглашения, и рекомендацию
`responsesCount / responsesRequired`. Для исследованного профиля 2026-09-07
окно составляло семь дней. Это platform counters, поэтому Job Agent сохраняет
их как временные snapshots, а не пытается вывести из локальных событий.

Наблюдение планируется обычным job:

```json
{
  "tag": "observe-primary-activity",
  "enabled": true,
  "triggers": [{
    "type": "cron",
    "expression": "*/30 * * * *",
    "timezone": "Europe/Moscow",
    "misfire": "run_once"
  }],
  "concurrency": "forbid",
  "action": {"type": "profile.activity.observe", "profile": "primary"}
}
```

Один запуск делает только `GET /applicant/profile/me`. Он не открывает чаты или
вакансии и не меняет платформенное состояние. Пропущенные интервалы, как и у
остальных scheduled jobs, схлопываются в один запуск.

`activity.observe` может собирать текущий score и объяснения для UI. Отдельный
workflow искусственного повышения score через пустые открытия, ожидание и
закрытие вакансий не создаётся. `vacancies.inspect` открывает страницу только
как реальную часть qualification pipeline: получить описание, требования,
работодателя, тест и принять решение об отклике.

Job Agent ведёт собственный durable-журнал подтверждённых действий, чтобы
сопоставлять их с наблюдаемой telemetry, не угадывая веса HH:

- `vacancy.inspected` — полная карточка прочитана перед qualification;
- `application.submitted` — адаптер подтвердил создание отклика;
- `conversation.message_sent` — transport подтвердил исходящее сообщение;
- `resume.touched` — web endpoint подтвердил поднятие резюме.

Идентичность записи выводится из `(platform, profile, kind, source_id)`, поэтому
retry или restart не увеличивает локальный счётчик повторно. Dashboard показывает
число и время последнего сигнала отдельно по профилю и виду действия. Эти
счётчики не называются score: факт действия не доказывает его текущий вес в
закрытом алгоритме HH.

`vacancy.inspected` также не переименовывается в `vacancy.viewed`. Текущий
HTTP/browser-state transport загружает полный HTML, но не исполняет страницу в
Chromium. Настоящий browser view можно фиксировать только после появления узкого
browser RPC, который выполняет осмысленный review и возвращает подтверждённый
результат навигации; произвольная задержка сама по себе таким результатом не
является.

Напоминания также не отправляются в произвольный чат ради score. Job
`conversation.follow_up.select` допускает только активный диалог, где последнее
сообщение исходило от соискателя и осталось без ответа дольше minimum silence;
выбор может идти от самого старого, самого нового либо стабильным `random`.
Входящий ответ, pending reminder, cooldown и лимит повторов исключают отправку.

Такое разделение пригодится, если HH введёт фильтр откликов без фактического
просмотра: audit trail подтвердит обычный inspect перед submit без имитации
пользовательской активности.

## Публикация/поднятие резюме

Живой web-контракт периодического поднятия:

```text
GET  /applicant/profile/me -> ResumeProfileFront-InitialState
POST https://resume-profile-front.hh.ru/profile/shards/resume/touch
body: {"hash":"<resume hash>"}
```

Initial state содержит `canTouch`, `nextTouchAt` и `update_timeout`. При живом
исследовании 2026-07-19 слишком ранний POST с корректным XSRF вернул `409`.
Scheduler использует `nextTouchAt`; период нельзя жёстко зашивать как четыре
часа. `resume.publish` для первой публикации/изменённого резюме остаётся
отдельной операцией.

Нормализация ошибок:

- `400` — невалидное состояние или payload;
- `409` — поднятие пока недоступно, перепланировать на `nextTouchAt`;
- `403` — неправильная роль или авторизация;
- `404` — резюме недоступно;
- `429` — ограничение частоты, перепланировать после server timestamp.

Платное автоматическое поднятие и ручной web touch — разные capabilities.
Нажимать upsell `resume-update-button_actions` нельзя.

## Skill verification

Каталог открывается на `/applicant/skill_verifications/methods`. Live-контракт
на 2026-09-12: страница содержит server-rendered карточки и полный
`HH-Lux-InitialState` (`<template id="HH-Lux-InitialState">`), из которого
читаются family ID, уровни и доступность видов. JSON path:
`skillsVerificationMethodsPage.items[]`; у элемента — `id`, `name`,
`category` (`SKILL`/`LANG`), `levels[]` с `internalId` (`base`/`middle`/
`advanced`/`a1`…), `rank`, `name` и объектами `theory`/`practice` с
`externalId` и `availability.status`. Карточки каталога больше не содержат
`<a href="/applicant/skills/<id>/verification_methods">`; на detail-страницу
переходит JS. Legacy-разметка с `skills-verification-method-container`,
`verification-method-title`, level tab и kind card остаётся fallback, если
initial state отсутствует.

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

Снятые живьём 2026-09-12 формы ответов:

```json
GET /shards/cert_tests/get_current_task
{
  "answers": [{"answer": "текст варианта", "uuid": "<answer-uuid>", "feature": "false"}],
  "description": "текст вопроса",
  "subType": "SINGLE",
  "taskId": 38051,
  "title": "Golang База 2.0",
  "media": []
}

POST /shards/cert_tests/submit_user_answer
{"userAnswerUuids": ["<answer-uuid>"], "taskId": 39416}
-> {"status": "ACCEPTED"}

GET /shards/contest/get_contest_tasks
{"contestTasks": [{"taskId": 39416, "status": "NOT_STARTED"}, ...]}
```

Онбординг ответов идёт через `get_current_task`: первый вопрос приходит с
server-rendered страницей `assessment.hh.ru/tests/<contest-id>`, последующие —
этим GET. Отдельного start-запроса нет: старт теории — это переход на
`assessment.hh.ru/tests/<family-id>` (в наблюдаемом случае `/tests/322406`,
family ID Golang), а практики — на `assessment.hh.ru/code/<family-id>`; контест
создаётся или возобновляется сервером на этом GET. Повторный GET при активном
месячном lock вернул `500 «Страница временно недоступна»`, то есть доступность
следующей попытки проверяется на этом же запросе. Практика — code-задачи
(3 задачи, 30 минут), и в проекте code-вопросы сознательно не входят в
submit-путь.

Результат попытки открывается на
`https://hh.ru/skills/applicant/contest_result?token=<token>`: статус
(`Навык не подтверждён`), уровень/вид, счёт `N из 10` и дата следующей попытки
(`Попробовать снова можно с 13 октября 2026` — месяц lock после использованной
попытки). Полный отчёт доступен по
`/skills/applicant/skills/<family-id>/<level-id>/report?category=SKILL`.


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
