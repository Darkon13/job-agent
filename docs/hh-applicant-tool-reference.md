# HH Applicant Tool: инвентарь для проектирования HH-адаптера

Источник анализа: локальная копия `~/hh-applicant-tool`, версия `1.8.17` на
момент просмотра. Документ фиксирует наблюдаемое поведение и контракты, а не
разрешение на копирование реализации.

## Лицензионная граница

`hh-applicant-tool` распространяется под собственной Limited Non-Commercial
License. Она разрешает личное изучение и модификацию без публикации изменений,
а включение в сторонний бесплатный некоммерческий продукт — только без изменения
исходного кода и с атрибуцией.

Поэтому Job Agent:

- использует проект как reference implementation;
- перенимает идеи, наблюдаемое поведение и API-контракты;
- пишет Go-код независимо;
- не копирует и не переводит Python-функции построчно;
- сохраняет ссылку и атрибуцию в документации;
- перед любым прямым использованием кода требует отдельной проверки лицензии.

## Найденные пользовательские операции

Платформенные capabilities, полезные для Job Agent:

| Операция утилиты | Наблюдаемое назначение | Целевая capability |
|---|---|---|
| `auth` | Авторизация через Playwright, сохранение token/cookies | `auth.login` |
| `refresh-token` | Обновление OAuth token | `auth.refresh` |
| `logout` | Отзыв token и очистка профиля | `auth.logout` |
| `whoami` | Текущий соискатель | `profile.get` |
| `list-resumes` | Список резюме аккаунта | `resumes.list` |
| `create-resume` | Создание резюме | будущая `resumes.create` |
| `clone-resume` | Клонирование резюме | будущая `resumes.clone` |
| `update-resumes` | Публикация/поднятие доступных резюме | `resumes.publish` |
| `apply-vacancies` | Поиск, фильтрация, письма, тесты, отклики | несколько workflows |
| `reply-employers` | Чтение и отправка сообщений | `conversations.*` |
| `clear-negotiations` | Отмена откликов, blacklist, web-only hide chat | admin capabilities |
| `clear-skipped` | Очистка локальной аналитики | не capability адаптера |
| `call-api` | Произвольный API-вызов | не выставлять обычным workflow |
| `query` | Произвольный SQL | не выставлять через MCP/UI |
| `settings/config` | Управление локальной конфигурацией | config subsystem |
| `ui` | Локальный pywebview UI | заменяется backend UI |

Install/uninstall, log и migrate-db являются обслуживающими операциями, а не
контрактами HH.

## Наблюдаемые HTTP/API операции

### Identity и auth

- `GET /me`;
- `POST /oauth/token` для получения/refresh token;
- `DELETE /oauth/token` для logout.

### Резюме

- `GET /resumes/mine`;
- `GET /resumes/{resume_id}`;
- `POST /resumes`;
- `POST /resume_profile` для клонирования/создания профиля резюме;
- `POST /resumes/{resume_id}/publish`.

### Вакансии и поиск

- `GET /vacancies` — глобальный поиск;
- `GET /vacancies/{vacancy_id}` — полная карточка;
- `GET /resumes/{resume_id}/similar_vacancies` — похожие вакансии;
- `PUT /vacancies/blacklisted/{vacancy_id}` — скрытие вакансии;
- `GET /employers/{employer_id}`;
- `GET/PUT /employers/blacklisted` и конкретный employer endpoint.

### Отклики и сообщения

- `POST /negotiations` — обычный отклик;
- `GET /negotiations` — список откликов/переговоров;
- `DELETE /negotiations/active/{negotiation_id}` — отмена;
- `GET /negotiations/{id}/messages` — история сообщений;
- `POST /negotiations/{id}/messages` — отправка сообщения.

### Web-only/hybrid операции в reference implementation

- получение vacancy test из страницы
  `/applicant/vacancy_response?...`;
- отправка test payload в `/applicant/vacancy_response/popup`;
- удаление/скрытие чата через `/applicant/negotiations/trash`;
- Playwright-авторизация и получение browser session;
- извлечение XSRF/cookies для web-запросов.

Транспортная классификация должна быть перепроверена при реализации: наличие
операции в старом коде не гарантирует, что endpoint и доступность не изменились.

## Источники поиска

В reference implementation источник выбирается неявно:

```text
если --search задан -> GET /vacancies
иначе               -> GET /resumes/{id}/similar_vacancies
```

Это поведение не переносится. В HH search object используется обязательное поле:

```json
{
  "source": "global"
}
```

или:

```json
{
  "source": "similar_resume",
  "resume": "backend-resume"
}
```

`global` является основным источником. `similar_resume` — самостоятельный search
object, который можно включить в routing или fallback. Официальный OpenAPI также
описывает `similar_vacancy` и contextual `related_vacancy`; подробнее см.
`docs/hh-api-contracts.md`.

## Фильтры HH search object

Из `apply-vacancies` найдены следующие параметры запроса:

| JSON-поле | Назначение |
|---|---|
| `text` | поисковая строка |
| `order_by` | `publication_time`, `salary_desc`, `salary_asc`, `relevance`, `distance` |
| `experience` | уровень опыта |
| `schedule` | график |
| `employment` | типы занятости |
| `area` | регионы |
| `metro` | станции метро |
| `professional_role` | профессиональные роли |
| `industry` | отрасли |
| `employer_id` | включённые работодатели |
| `excluded_employer_id` | исключённые работодатели; source-specific, сверять с OpenAPI |
| `currency` | валюта зарплаты |
| `salary` | минимальная зарплата |
| `only_with_salary` | только с зарплатой |
| `label` | platform labels |
| `period` | глубина поиска в днях |
| `date_from`, `date_to` | диапазон публикации |
| `top_lat`, `bottom_lat` | широта bounding box |
| `left_lng`, `right_lng` | долгота bounding box |
| `sort_point_lat`, `sort_point_lng` | точка сортировки по расстоянию |
| `search_field` | поля полнотекстового поиска |
| `no_magic` | отключение автоматической интерпретации текста |
| `premium` | premium-only выдача |
| `page`, `per_page` | пагинация; runtime-owned |

`page` и `per_page` не являются пользовательскими фильтрами домена. Ими должен
управлять HH search service/scheduler. Ограничение глубины можно задавать
отдельной orchestration policy (`max_pages`, `max_items`, deadline).

Перед фиксацией enum и cardinality каждого поля следует свериться с актуальной
официальной спецификацией HH API.

Расширенная и сверенная с локальным OpenAPI snapshot таблица находится в
`docs/hh-api-contracts.md`.

## Параметры, не принадлежащие search query

В исходной операции флаги поиска смешаны с поведением workflow. В Job Agent они
разносятся по объектам:

| Флаг reference implementation | Целевой объект |
|---|---|
| `--resume-id` | search target/application target |
| `--letter-file`, `--force-message` | cover-letter policy |
| `--use-ai`, prompts, AI rate limit | operator configuration |
| `--ai-filter` | vacancy filter pipeline |
| `--excluded-filter` | deterministic filter operator |
| `--send-email` | optional notification/outreach workflow |
| `--skip-tests` | application/test policy |
| `--dry-run` | execution policy |
| `--total-pages`, `--per-page` | pagination/depth policy |
| `--max-responses` | vacancy/application policy; в reference не реализован |

## Поведение vacancy application workflow

Полезные наблюдаемые шаги, которые можно воспроизвести независимой реализацией:

1. Получить опубликованные резюме и вакансии.
2. Не обрабатывать archived и уже имеющие `relations` вакансии.
3. Применить локальные deterministic/LLM filters.
4. Получить полную карточку вакансии и работодателя по необходимости.
5. Сформировать сопроводительное, если оно обязательно или требует policy.
6. Если `has_test`, извлечь тест и передать его test workflow.
7. Иначе отправить `POST /negotiations`.
8. Нормализовать limit, redirect, captcha и API errors.
9. Сохранить application state и результат независимо от исхода.

В Job Agent шаги оформляются событиями и задачами, а не одним большим методом.

## Тесты

Reference implementation извлекает `vacancyTests`, проходит tasks и отправляет
web form payload с XSRF. Ответы генерируются AI или эвристикой.

Job Agent заменяет эвристику на operator routing:

```text
receive current question
-> normalize + question fingerprint
-> known qualification AnswerBlock
-> model operator
-> pending/manual fallback
```

Нельзя привязывать сохранённые ответы к внутреннему task ID: одинаковый тест в
разных вакансиях может получить другие IDs. Сопоставление строится по
нормализованному вопросу, вариантам и типу ответа.

## Чаты агрегатора

Наблюдаемый workflow:

- получить negotiations;
- отфильтровать по resume/state/period/invitation;
- постранично загрузить сообщения;
- определить автора последнего сообщения;
- сформировать ответ по шаблону, через AI или вручную;
- отправить сообщение;
- опционально отменить отклик или заблокировать работодателя.

В Job Agent чтение чата отделяется от обработки содержимого. Новое сообщение
создаёт conversation event, после чего classifier выбирает handler:

- обычный ответ;
- тест;
- опросник;
- назначение созвона;
- неизвестное сообщение/pending.

Назначение созвона может активировать optional calendar integration. Остальные
типы являются частью platform conversation workflow.

## Что не переносить как архитектуру

- переключение global/similar по наличию поискового текста;
- один большой `apply_vacancies` orchestration object;
- CLI-флаги как runtime config;
- cron, повторно запускающий утилиту;
- отдельный контейнер на каждый профиль;
- случайные/эвристические ответы на неизвестные тесты;
- произвольный API или SQL как обычный MCP tool;
- обход captcha моделью;
- хранение platform-specific аналитики в core-таблицах.

## Captcha в reference implementation

При авторизации reference implementation ждёт
`account-captcha-picture`, делает screenshot элемента и выводит PNG через Kitty
graphics protocol либо самостоятельно кодирует его в Sixel. Пользователь вводит
текст в терминале, после чего Playwright заполняет `account-captcha-input`.

При отклике API error parser преобразует `403` с `captcha_required` в отдельную
ошибку и извлекает `captcha_url`. Реализация открывает URL в новом headless
browser, распознаёт изображение Vision-моделью, переносит cookies обратно в HTTP
session и повторяет `POST /negotiations`.

Переносим полезное разделение на captcha detection, presentation и submission,
но не переносим автоматическое распознавание и новый изолированный browser
context. Job Agent публикует manual challenge, использует context исходного
профиля и после успешного прохождения безопасно повторяет исходную команду.
