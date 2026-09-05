# HH API: контракты первого адаптера

Документ описывает доступный API-first слой HH-адаптера и границу перехода к
browser transport.

## Источники анализа

- официальный репозиторий `hhru/api`, локальная копия `~/hh/api`, последний
  локальный commit от 2025-12-19;
- live OpenAPI `api.hh.ru/openapi/redoc`, повторно сверенный 5 сентября 2026;
- публичный OpenAPI snapshot из `~/hh-applicant-tool/docs/hhapi/openapi.yml`;
- reference implementation `~/hh-applicant-tool`.

Репозиторий `hhru/api` теперь в основном содержит ссылки на live OpenAPI, а не
саму спецификацию. Перед реализацией каждого следующего endpoint контракт нужно
повторно сверять с текущей официальной документацией и реальным applicant
token: сохранённый snapshot полезен для навигации, но не является источником
актуальности.

## Транспортная граница

### API-first

Через публичный API предполагается выполнять:

- получение текущего пользователя;
- чтение, создание, обновление и публикацию резюме;
- глобальный и recommendation-поиск вакансий;
- получение полной вакансии и работодателя;
- получение подходящих для вакансии резюме;
- обычный отклик без browser-only формы;
- получение negotiations и их сообщений;
- отправку сообщения в negotiation;
- чтение assessments, приложенных к сообщению;
- favorite/blacklist вакансий и работодателей;
- справочники, регионы, метро, industries и professional roles;
- search suggestions.

### Browser/hybrid

Browser transport нужен для:

- интерактивной OAuth-авторизации и получения пользовательской сессии;
- vacancy tests, для которых публичный applicant API не предоставляет submit;
- assessments/опросников из чата, если действие ведёт на `alternate_url`;
- новых возможностей чатов, отсутствующих в legacy negotiations API;
- direct application по внешнему `response_url`;
- platform flow, который API возвращает как redirect или
  `ValidationRequired`.

Документация HH прямо помечает applicant negotiation messages как устаревший
интерфейс, который может не отражать новые возможности чатов. Поэтому
conversation poller должен уметь дополняться browser observation, не меняя
доменный `ConversationEvent`.

## Поисковые endpoints

| Source | Endpoint | Reference object |
|---|---|---|
| `global` | `GET /vacancies` | отсутствует |
| `similar_resume` | `GET /resumes/{resume_id}/similar_vacancies` | resume |
| `similar_vacancy` | `GET /vacancies/{vacancy_id}/similar_vacancies` | vacancy |
| `related_vacancy` | `GET /vacancies/{vacancy_id}/related_vacancies` | vacancy + request context |

Это таблица public API transport, а не полный список browser routes. В текущем
web UI `similar_resume` открывается как `/search/vacancy?resume=<resume-id>`, а
global search — как тот же route без `resume`. Живой browser contract описан в
`hh-browser-search.md`.

`related_vacancy` отличается от строгого similarity: результат может зависеть
от applicant authorization, последнего резюме и `hhtmSource`. Его результаты
нельзя считать воспроизводимыми только по vacancy ID.

### Пагинация

- `page` начинается с `0`;
- `per_page` по умолчанию `10`, максимум `100`;
- максимальная глубина выдачи — `2000` элементов;
- `page` и `per_page` принадлежат runtime search cursor, а не пользовательскому
  query object;
- search result содержит `items`, `found`, `page`, `pages`, `per_page`;
- дополнительно могут возвращаться `clusters`, `arguments`, `fixes`, `suggests`
  и `alternate_url`.

Реализованный global search запрашивает `per_page=100` и прекращает пагинацию
по `pages`, пустому `items` или глубине 2000 — что наступит раньше. Номер
страницы кодируется непрозрачным для workflow cursor; пользовательский query не
может подменить `page` или `per_page`.

Каждый configured search имеет строку `search_runs` в SQLite schema v6:
definition, search profile, target profiles, query, correlation ID, cursor,
`done` и revision. Одна `vacancy.search_page` task обрабатывает ровно одну
страницу. Cursor продвигается только после всех idempotent writes страницы;
повтор после частичного сбоя безопасен благодаря ключам vacancy/discovery/
application/task. Если процесс остановился между сохранением cursor и enqueue
следующей страницы, повтор старой page task видит расхождение cursor и
восстанавливает актуальную задачу.

`429` сохраняет `Retry-After` в `OperationError`: worker переводит задачу в
`retry_scheduled`, снимает lease и не держит goroutine во время ожидания.

## Поля HH search object

### Текст и классификация

| JSON | Тип | Повторяемый | Источник значения |
|---|---|---:|---|
| `text` | string | нет | язык поисковых запросов HH |
| `search_field` | string[] | да | `vacancy_search_fields` из `/dictionaries` |
| `experience` | string[] | да | `experience` из `/dictionaries` |
| `professional_role` | string[] | да | `/professional_roles` |
| `industry` | string[] | да | `/industries` |
| `employer_id` | string[] | да | employer ID |
| `excluded_employer_id` | string[] | да | подтверждено live OpenAPI для global search |
| `excluded_text` | string | нет | список исключаемых слов через запятую |
| `education` | string[] | да | `not_required_or_not_specified`, `special_secondary`, `higher` |

### Местоположение

| JSON | Тип | Условие |
|---|---|---|
| `area` | string[] | ID из `/areas` |
| `metro` | string[] | ID веток/станций из `/metro` |
| `top_lat` | number | передаются все четыре bounding-box поля |
| `bottom_lat` | number | передаются все четыре bounding-box поля |
| `left_lng` | number | передаются все четыре bounding-box поля |
| `right_lng` | number | передаются все четыре bounding-box поля |
| `sort_point_lat` | number | вместе с `sort_point_lng`, обычно для `order_by=distance` |
| `sort_point_lng` | number | вместе с `sort_point_lat` |

### Формат работы

| JSON | Тип | Статус/источник |
|---|---|---|
| `employment_form` | string[] | актуальный `employment_form` dictionary |
| `work_schedule_by_days` | string[] | dictionary |
| `working_hours` | string[] | dictionary |
| `work_format` | string[] | dictionary |
| `accept_temporary` | boolean | только временная работа |
| `employment` | string[] | deprecated, использовать `employment_form` |
| `schedule` | string[] | deprecated, использовать новые work fields |
| `part_time` | string[] | deprecated aggregate filter |

В runtime domain необходимо сохранять неизвестные dictionary IDs: новые
значения HH не должны требовать релиза приложения, если структура поля не
изменилась.

### Зарплата и дата

| JSON | Тип | Условие |
|---|---|---|
| `salary` | number | искомая зарплата, не строгая нижняя граница |
| `currency` | string | имеет смысл вместе с `salary`; default `RUR` |
| `only_with_salary` | boolean | исключить вакансии без вилки |
| `period` | integer | глубина в днях |
| `date_from` | ISO-8601 string | несовместимо с `period` |
| `date_to` | ISO-8601 string | несовместимо с `period` |

`salary` нельзя трактовать в core как гарантированную минимальную зарплату: API
ищет вакансии с близкой вилкой и конвертирует валюты. Строгий порог реализуется
отдельным post-search filter по `salary_range`.

### Сортировка и параметры ответа

| JSON | Тип | Назначение |
|---|---|---|
| `order_by` | string | `vacancy_search_order` из `/dictionaries` |
| `label` | string[] | `vacancy_label` из `/dictionaries` |
| `only_with_salary` | boolean | фильтр выдачи |
| `no_magic` | boolean | отключить автоматический разбор `text` |
| `premium` | boolean | учитывать premium-позиционирование |
| `clusters` | boolean | вернуть search clusters |
| `describe_arguments` | boolean | вернуть разобранные arguments |
| `responses_count_enabled` | boolean | вернуть counters откликов |

`clusters`, `describe_arguments` и `responses_count_enabled` влияют на ответ, а
не на релевантность. Позже их можно вынести из `SearchQuery` в adapter execution
options, если появится отдельный стабильный контракт.

### Source-specific ограничения

OpenAPI snapshot обновлён неравномерно:

- global search содержит новые employment/work fields;
- similar/related endpoints документируют старый набор и
  `excluded_employer_id`;
- одинаково названное поле может появиться в live API раньше обновления всех
  endpoint schemas.

HH adapter должен иметь матрицу поддерживаемых полей по source. Неизвестное или
неподдерживаемое поле вызывает config validation error, а не молча игнорируется.
Текущая реализация строго декодирует JSON (`DisallowUnknownFields`), проверяет
непустые и неповторяющиеся значения повторяемых фильтров, даты, диапазоны
координат, `period=1..30` и зависимые группы полей. Исполнение пока включено
только для `global`; остальные source возвращают `Unsupported` и не маскируются
под пустую выдачу.

## Основные сущности

### Applicant profile

`GET /me` даёт identity и applicant counters. В core хранится минимальная
identity-ссылка; персональные поля остаются platform payload и secret-bearing
storage.

Реализованный read client передаёт OAuth token как `Authorization: Bearer`,
обязательный `HH-User-Agent` и сохраняет из ответа только внешний account ID и
тип авторизации. `401/403` нормализуются как `Unauthorized`, `429` как
`RateLimited`, `5xx` и transport failure как `TemporaryFailure`, остальные
неуспешные ответы как `PermanentFailure`. Тело ошибки не включается в доменную
ошибку, чтобы ответ платформы не мог протащить credential или персональные
данные в лог. Контракт повторно сверен с официальной документацией HH
5 сентября 2026 года.

### Resume

Ключевые поля списка `/resumes/mine`:

- `id`, title и alternate URL;
- status, `blocked`, `finished`, `can_publish_or_update`;
- `next_publish_at`;
- access/visibility;
- total/new views;
- `similar_vacancies.url` и counters;
- employment terms и applicant actions.

Profile (учётная запись) и Resume — разные сущности. Один profile содержит
несколько resumes. Search target должен ссылаться на оба, если source или
application требует конкретное резюме.

### Vacancy search item

Поисковый item содержит достаточно данных для первичной фильтрации:

- identity: `id`, `name`, URLs;
- employer, department, area, address/metro;
- `salary_range` (`salary` deprecated);
- experience, professional roles;
- employment/work format/schedule fields;
- publication date и archive state;
- `has_test`, `response_letter_required`, `response_url`;
- applicant `relations`;
- snippets requirement/responsibility;
- optional response counters.

Перед окончательным решением или генерацией письма загружается полная вакансия.

### Full vacancy

Дополнительно содержит:

- HTML description и key skills;
- contacts и employer details;
- languages и driver licence requirements;
- `allow_messages`;
- `test` metadata;
- `negotiations_url` и `suitable_resumes_url` для applicant token;
- vacancy properties и ограничения для соискателя.

Deprecated поля (`salary`, `employment`, `schedule`, `type`) маппятся как
fallback. Приоритет отдается `salary_range`, `employment_form`, work fields,
`closed_for_applicants` и `vacancy_properties`.

### Application/Negotiation

Обычный отклик:

```text
POST /negotiations
Content-Type: multipart/form-data
resume_id, vacancy_id, optional message (max 10000)
```

Результат:

- `201` + `Location: /negotiations/{id}` — создан negotiation;
- `303` + внешний Location — direct application, передать browser workflow;
- `400` — validation;
- `403` — forbidden/limit/auth/application restriction.

Перед откликом можно вызвать
`GET /vacancies/{vacancy_id}/suitable_resumes`. `Application` уникален для пары
profile/resume/vacancy согласно platform semantics и локальной idempotency
policy.

Negotiation содержит:

- ID, created/updated timestamps и state;
- source и messaging status;
- vacancy и resume;
- `has_updates`, counters и `viewed_by_opponent`;
- `applicant_question_state`;
- phone calls/job-search metadata при запросе соответствующих полей.

### Message и Assessment

Message содержит ID, автора (`applicant|employer`), текст, state, timestamps,
read/view flags и `assessments`.

Текущий web chat дополняет deprecated public message API отдельным `chatik`
contract: structured suggestions, text/event buttons, workflow transition,
write state и test solution resources. Он описан в `hh-chat-contract.md` и
реализуется browser/web transport внутри HH adapter.

Assessment — API-представление теста или опросника в чате:

- `id`, `name`;
- список `actions`;
- action содержит `id`, `enabled`, `disable_reason`, `alternate_url`.

Conversation poller преобразует новый assessment в
`questionnaire.discovered`/`test.discovered`. `alternate_url` является входом
browser extractor, но не доменным ID теста.

### Employer

Для core достаточно `id`, name, URLs, trusted/blacklisted и logo/rating metadata.
Полная карточка может использоваться для фильтрации и контекста письма.

### Dictionaries

Adapter загружает и кэширует:

- `/dictionaries`;
- `/areas`;
- `/metro`;
- `/professional_roles`;
- `/industries`;
- `/languages`;
- `/skills`;
- relevant `/suggests/*` endpoints.

Config UI должен показывать человекочитаемые значения, но сохранять стабильные
HH IDs. Кэш справочников имеет TTL и обновляется независимо от search cache.

## Capabilities первого HH-адаптера

### Обязательные для первого среза

- `auth.login`, `auth.refresh`, `profile.get`;
- `resumes.list`, `resumes.get`, `resumes.publish`;
- `vacancies.search.global`;
- `vacancies.search.similar_resume`;
- `vacancies.get`;
- `vacancies.suitable_resumes`;
- `applications.apply`;
- `negotiations.list`, `negotiations.get`;
- `messages.list`, `messages.send`;
- `dictionaries.read`.

### Следующий слой

- vacancy/related search modes;
- favorite и blacklist;
- resume create/edit/delete/visibility;
- saved vacancy searches;
- hide negotiation;
- message edit;
- browser extraction/submission of vacancy tests;
- browser processing of chat assessments;
- conversation event polling.

Подробный API-first контракт resume-profile, поля редактора и publish scheduler
вынесены в `hh-resume-contract.md`; живой web flow отклика и обязательных
vacancy questionnaires — в `hh-browser-operations.md`.

## Нормализованные ошибки

HTTP и HH error payload маппятся в core:

| HH/HTTP | Core category |
|---|---|
| invalid/required argument, `400` | `ValidationRequired` или `PermanentFailure` |
| expired/invalid token, auth error | `Unauthorized` |
| captcha response | `ConfirmationRequired` |
| application redirect `303` | `Unsupported` для API transport, затем browser workflow |
| limit/quota/rate limit, `429` | `RateLimited` |
| transient `5xx`/network | `TemporaryFailure` |
| not found/archived/no access | `PermanentFailure` после проверки причины |

Adapter сохраняет исходный status, HH error type/value и request ID в
диагностических metadata, не раскрывая token и персональные данные.
