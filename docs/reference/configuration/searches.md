# `searches`

Поиски, из которых router берёт вакансии. Один поиск — один запрос к
платформе; `query` валидируется адаптером, потому что фильтры у платформ
разные.

## Пример

```json
"searches": [
  {
    "tag": "golang-similar",
    "adapter": "hh-main",
    "profiles": ["primary"],
    "priority": 100,
    "target_applications": 50,
    "query": {
      "source": "similar_resume",
      "resume": "0123456789abcdef",
      "area": ["1"],
      "text": "Golang AND (Kafka OR PostgreSQL)",
      "page_size": 20,
      "max_pages": 10
    }
  }
]
```

| Поле | Тип | Обяз. | Описание |
|---|---|---|---|
| `tag` | string | **да** | Имя поиска; на него ссылаются `jobs[].action.routes`. |
| `adapter` | string | **да** | `tag` адаптера; у всех route одного campaign должен совпадать. |
| `profiles` | array | **да** | Профили-получатели: по каждому найденая вакансия становится откликом. |
| `priority` | number | нет | Приоритет задач поиска, `-1000..1000`. Router берёт route с большим приоритетом первым. |
| `target_applications` | number | **да** | Сколько откликов кампания планирует получить из этого поиска. |
| `fallback` | string | нет | `tag` следующего поиска, если этот не набрал цель. Циклы запрещены. |
| `query` | object | **да** | Запрос к платформе (см. ниже). |

## `query` для `hh`

### Выбор источника

| Поле | Тип | Обяз. | Описание |
|---|---|---|---|
| `source` | string | **да** | `global` — глобальная выдача; `similar_resume` — вакансии, подходящие к резюме; `similar_vacancy` — к конкретной вакансии; `related_vacancy` — контекстная выдача платформы. |
| `resume` | string | да для `similar_resume` | ID резюме. Для `global` и vacancy-источников запрещён. |
| `vacancy` | string | да для `similar_vacancy`/`related_vacancy` | ID исходной вакансии. |

`similar_resume` — правильный источник для подбора под резюме: browser-транспорт
открывает web-поиск с chip «Резюме» (`/search/vacancy?resume=<id>`). Не задавайте
`order_by: publication_time` вместе с ним — это отключает релевантную
сортировку HH и возвращает общий поток.

### Текст и семантика

| Поле | Тип | Обяз. | Описание |
|---|---|---|---|
| `text` | string | нет | Поисковый запрос. Понимает синтаксис HH: `AND`, `OR`, `NOT`, кавычки для точной фразы, скобки, например `Golang AND (Kafka OR PostgreSQL) NOT руководитель`. |
| `search_field` | array | нет | Где искать `text`: `name`, `description`, `company_name`. |
| `excluded_text` | string | нет | Платформенный фильтр исключений по тексту (аналог `NOT`). |

### Фильтры вакансии

| Поле | Тип | Обяз. | Описание |
|---|---|---|---|
| `area` | array | нет | ID регионов (`1` — Москва, `2` — Санкт-Петербург). |
| `metro` | array | нет | ID станций метро. |
| `experience` | array | нет | `noExperience`, `between1And3`, `between3And6`, `moreThan6`. |
| `employment` | array | нет | `full`, `part`, `project`, `volunteer`, `probation`. |
| `employment_form` | array | нет | `FULL`, `PART`, `PROJECT`, `PROBATION`, `VOLUNTEER` (современное поле). |
| `schedule` | array | нет | `fullDay`, `shift`, `flexible`, `remote`, `flyInFlyOut`. |
| `work_schedule_by_days` | array | нет | `FIVE_ON_TWO_OFF`, `SIX_ON_ONE_OFF`, `TWO_ON_TWO_OFF`, `OTHER`. |
| `working_hours` | array | нет | `HOURS_2`, `HOURS_4`, `HOURS_6`, `HOURS_8`, `HOURS_10`, `HOURS_12`, `FLEXIBLE`. |
| `work_format` | array | нет | `ON_SITE`, `REMOTE`, `HYBRID`, `FIELD_WORK`. |
| `professional_role` | array | нет | ID профессиональных ролей (например, `96` — программист). |
| `industry` | array | нет | ID отраслей работодателя. |
| `employer_id` | array | нет | ID компаний. |
| `excluded_employer_id` | array | нет | Исключить компании. |
| `label` | array | нет | Метки вакансии (`accredited_it`, `high_demand` и т.п.). |
| `education` | array | нет | Требуемый уровень образования. |
| `part_time` | array | нет | Подработка: `INCLUDE`, `ONLY`. |
| `accept_temporary` | bool | нет | Включать временные вакансии. |
| `only_with_salary` | bool | нет | Только с указанной зарплатой. |
| `salary` | number | нет | Порог зарплаты; требует `currency`. |
| `currency` | string | нет | `RUR`, `USD`, `EUR`, `KZT` и т.п. |

Современные work-поля (`employment_form`, `work_schedule_by_days`,
`working_hours`, `work_format`) классический API-эндпоинт similar не знает:
если они заданы, адаптер использует browser-поиск с полным набором фильтров
(нужна browser-сессия), иначе вернёт `unsupported`.

!!! warning "Ограничения по источникам"
    - `similar_vacancy` и `related_vacancy` пока умеют работать только через
      OAuth/API-сессию: для browser-cookie профилей они возвращают
      `unsupported`.
    - `related_vacancy` платформа принимает только с пагинацией; остальные
      фильтры к нему неприменимы — используйте клиентские `qualification`
      профиля.
    - `similar_resume` — источник для подбора под резюме; он же единственный
      similar-источник, доступный browser-профилям.

### Даты, география, сортировка

| Поле | Тип | Обяз. | Описание |
|---|---|---|---|
| `period` | number | нет | Свежесть в днях, 1..30. Нельзя вместе с `date_from`/`date_to`. |
| `date_from`, `date_to` | string | нет | Границы публикации (`YYYY-MM-DD`). |
| `top_lat`, `bottom_lat`, `left_lng`, `right_lng` | number | нет | Прямоугольник поиска по карте; задаются все четыре. |
| `sort_point_lat`, `sort_point_lng` | number | нет | Точка сортировки по удалённости. |
| `order_by` | string | нет | `publication_time`, `salary_desc`, `salary_asc`, `relevance`, `distance`. |

### Пагинация и редкие флаги

| Поле | Тип | Обяз. | По умолчанию | Описание |
|---|---|---|---|---|
| `page_size` | number | нет | `100` (API) / `20` (browser) | Вакансий на страницу, 1..100. |
| `max_pages` | number | нет | `1` (browser) | Максимум страниц за один прогон, до глубины HH (~2000 вакансий). |
| `clusters` | bool | нет | `false` | Вернуть кластеры фильтров. |
| `describe_arguments` | bool | нет | `false` | Подробное описание аргументов. |
| `no_magic` | bool | нет | `false` | Отключить «магические» подсказки HH. |
| `premium` | bool | нет | `false` | Только премиум-вакансии. |
| `responses_count_enabled` | bool | нет | `false` | Вернуть число откликов. |

## Поведение при изменении

Изменение `query`, `profiles`, `adapter` или платформы автоматически начинает
новую generation run-а: старый курсор относится к другой выдаче и очищается,
дедупликация вакансий и откликов не даёт повторных действий. Участие оператора
не требуется.

## Связанные документы

- [HH: поиск](../../hh-browser-search.md) — живой web-контракт и фильтры;
- [Роутинг и desired state](../../profile-desired-state-and-routing.md) —
  приоритеты и fallback;
- [Отклики](applications.md) — клиентские фильтры профиля.
