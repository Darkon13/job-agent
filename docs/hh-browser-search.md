# HH: browser search contract

Наблюдения сделаны 2026-07-18 в авторизованной applicant web session. Resume,
vacancy и account IDs не фиксируются.

## Два пользовательских маршрута

Текущий web UI использует одну страницу `/search/vacancy`, а режим определяется
query context.

| Семантика | Вход в UI | Browser URL |
|---|---|---|
| Similar для резюме | карточка резюме, `resume-recommendations__button_updateResume` | `/search/vacancy?resume=<resume-id>&from=resumelist...` |
| Global | верхняя кнопка `searchVacancy-button` | `/search/vacancy?from=header-menu...` без `resume` |

`/applicant/resumes` в текущем UI перенаправляет на `/applicant/profile/me`.
Карточка резюме имеет `data-qa="resume"`; ссылка самой карточки использует
`resume-card-link-*`.

Это уточняет термин `similar_resume`: на уровне продукта это семантический
источник вакансий, а не обещание использовать конкретный endpoint. HH adapter
может получить его двумя transport-вариантами:

- API: `GET /resumes/{id}/similar_vacancies`;
- browser: `GET /search/vacancy?resume=<id>`.

Browser-вариант важен для проверки актуальной web-семантики и новых фильтров.
API-вариант предпочтителен, когда его контракт покрывает запрос.

## Resume context

В similar-выдаче наблюдались:

- query parameter `resume`;
- hidden form input `name=resume`;
- выбранный chip `data-qa="resume-context-chip"`;
- тот же основной GET form `/search/vacancy`, что и у global search.

В текущей версии страницы resume chip имеет `disabled` и не удаляется кликом.
Верхняя кнопка «Поиск» открывает global route: `resume` исчезает из URL, формы и
DOM. Старые версии UI могли позволять выключить resume context прямо в фильтрах,
поэтому adapter определяет режим по фактическому URL/form state, а не по наличию
конкретной кнопки удаления.

## Search form и filters drawer

Основные locators:

| Элемент | `data-qa` |
|---|---|
| Search text | `search-input` |
| Submit | `search-button` |
| Region chip | `search-filter-area-chip` |
| Work schedule chip | `search-filter-work_schedule_by_days-chip` |
| Working hours chip | `search-filter-working_hours-chip` |
| Experience chip | `search-filter-experience-chip` |
| Employment chip | `search-filter-employment_form-chip` |
| Compensation chip | `search-filter-compensation_per_mode-chip` |
| Open all filters | `header-search-filters-button` |
| Filters dialog | `search-filters`, role `dialog` |
| Reset | `search-drawer-filters-reset` |
| Apply | `search-drawer-filters-submit` |

В открытом drawer наблюдались следующие смысловые поля:

- `text`, `search_field` (`name`, `company_name`, `description`);
- area (`filter-select-area`);
- `work_schedule_by_days`, `working_hours`, `night_shifts`;
- `experience`, `work_format`;
- compensation amount/currency, `compensation_mode`, `with_salary`,
  `compensation_frequency`;
- `employment_form`, part-time/GPH flag, `internship`;
- professional role (`search-filter-professional-role-trigger`);
- company (`filter-select-company`) и industry
  (`search-filter-industry-trigger`);
- `excluded_text`;
- driver licence (`filter-select-driver_license_types`);
- `education`, `label`, `inclusiveness_types`.

Некоторые React controls не имеют итогового query `name` прямо в DOM. Их нельзя
сериализовать по input attributes вслепую: browser transport должен выставить
значение через control и после navigation сверить получившийся URL. Adapter
search schema хранит смысловые HH-поля, а не внутренние React IDs.

## Выдача и пагинация

Наблюдаемый server-rendered result contract:

| Элемент | `data-qa` |
|---|---|
| Result count | `vacancies-search-header` |
| Vacancy card | `vacancy-serp__vacancy` |
| Vacancy title/link | `serp-item__title`, URL `/vacancy/<id>` |
| Employer | `vacancy-serp__vacancy-employer` |
| Address | `vacancy-serp__vacancy-address` |
| Apply link | `vacancy-serp__vacancy_response` |
| Pagination | `pager-block`, `pager-page`, `pager-next` |

На странице было 50 карточек. Pager показывал глубину до 40 страниц, то есть
2000 результатов — тот же практический предел, который документирован для
public vacancy search API. `page` участвует в URL; `search_session_id` является
эпhemeral web context и не входит в декларативный config.

Для сбора данных HH adapter по возможности использует public API и нормализует
результат. Browser parsing нужен как fallback/compatibility transport, но его
selectors и pagination contract тестируются fixtures отдельно от core.

На 2026-09-06 анонимный `GET https://api.hh.ru/vacancies` с этой рабочей сети
сразу получал anti-bot `403`, в то время как тот же global search в
авторизованной web-сессии возвращал выдачу. Поэтому реализованный fallback
повторяет только GET `/search/vacancy` с HH cookies из Playwright storage state,
извлекает карточки по `data-qa` и отдельно читает полную страницу вакансии.
Параметры `page_size` и `max_pages` являются локальными предохранителями: они
ограничивают число создаваемых applications даже если UI вернул больше
карточек. Для browser transport значения по умолчанию — 20 карточек и одна
страница.

## Инварианты adapter

- `source=global` никогда не добавляет resume reference.
- `source=similar_resume` всегда требует resume reference.
- Semantic source и transport (`api`, `browser`, `auto`) — разные решения.
- Tracking parameters (`from`, `hhtmFrom*`, `search_session_id`) не являются
  пользовательскими фильтрами.
- Search result дедуплицируется по `(platform, vacancy_id)` независимо от source
  и transport.
- Переход по apply link не выполняется во время search/extraction.
- Browser reader не реализует submit-интерфейс и используется только для
  `dry_run`.
