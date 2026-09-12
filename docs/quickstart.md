# Quickstart: конфиг → работающие отклики

Job Agent управляется декларативной конфигурацией в духе Xray и sing-box. Вы
описываете адаптер, профили, поиски и jobs — дальше сервис сам ищет вакансии,
готовит письма, отправляет отклики, проходит анкеты и по расписанию поднимает
резюме. Dashboard и интерактивный вход через браузер — **опциональные**
удобства: без них достаточно конфига и одного сохранённого входа.

Готовый стартовый конфиг лежит в
[`deploy/config.example.json`](../deploy/config.example.json): один профиль,
два поиска с fallback, ежедневное автоподнятие резюме и ежедневная рассылка
откликов. Ниже — как его запустить и что означают блоки.

## 1. Настройте конфиг

```sh
git clone https://github.com/Darkon13/job-agent.git
cd job-agent
cp deploy/config.example.json deploy/config.json
mkdir -p data
```

Откройте `deploy/config.json` и замените `replace-with-hh-resume-id` на ID
своего резюме HH (виден в ссылке на резюме в кабинете). Имя профиля `main` —
это **ваш произвольный тег объекта**: назовите его как угодно (`backend`,
`account-1`, `hh-main`), главное — используйте одно имя во всех ссылках.

Ключевые блоки стартового конфига:

```jsonc
{
  "adapters": [{"tag": "hh-main", "type": "hh"}],
  "profiles": [{
    "tag": "main",                       // ваш тег профиля
    "adapter": "hh-main",
    "resume": "replace-with-hh-resume-id",
    "state_file": "/data/profiles/main.json",
    "applications": {
      "mode": "dry_run",                 // сначала безопасный режим
      "message_template_file": "messages/backend.json",
      "qualification": {"include_any": ["Go", "Golang", "Backend"]},
      "timezone": "Europe/Moscow"
    },
    "conversations": {"allow_send": false, "allow_mark_read": false}
  }],
  "searches": [
    {
      "tag": "golang-global",
      "adapter": "hh-main",
      "profiles": ["main"],
      "priority": 100,
      "target_applications": 20,
      "fallback": "golang-similar",      // когда выдача исчерпана
      "query": {"source": "global", "text": "Golang developer", "area": ["1"], "page_size": 20, "max_pages": 2}
    },
    {
      "tag": "golang-similar",
      "adapter": "hh-main",
      "profiles": ["main"],
      "priority": 50,
      "query": {"source": "similar_resume", "resume": "replace-with-hh-resume-id", "area": ["1"]}
    }
  ],
  "jobs": [
    {
      "tag": "touch-main-resume",
      "triggers": [
        {
          "type": "cron",
          "expression": "0 10 * * *",
          "timezone": "Europe/Moscow"
        }
      ],
      "action": {
        "type": "resume.touch",
        "profile": "main"
      }
    },
    {
      "tag": "daily-applications",
      "triggers": [
        {
          "type": "cron",
          "expression": "30 9 * * *",
          "timezone": "Europe/Moscow"
        }
      ],
      "action": {
        "type": "application.campaign",
        "profiles": ["main"],
        "routes": ["golang-global", "golang-similar"],
        "target_successful": 20,
        "max_in_flight": 2
      }
    }
  ]
}
```

Что здесь происходит:

- **`searches`** — что искать. `source: global` — обычная выдача;
  `source: similar_resume` — похожие на ваше резюме. `fallback` задаёт
  следующий поиск, если текущая выдача закончилась, а цель по откликам не
  достигнута.
- **`jobs`** — расписание. `resume.touch` поднимает резюме (в примере — раз в
  день), `application.campaign` запускает отклики по маршрутам `routes` в
  порядке приоритета.
- **`profiles`** — аккаунты. Фильтры `qualification` и тексты писем живут в
  профиле, поэтому у разных аккаунтов могут быть разные правила.
- Сообщения лежат рядом с конфигом: `deploy/messages/backend.json`.

Ключи `query` у HH-поиска:

| Ключ | Что означает |
|---|---|
| `source` | `global` — обычная выдача, `similar_resume` — похожие на ваше резюме, `similar_vacancy`/`related_vacancy` — похожие на конкретную вакансию |
| `text` | поисковый запрос, например `Golang developer` |
| `area` | **регион**: числовой ID региона HH. `"1"` — Москва, `"2"` — Санкт-Петербург; полный список — `https://api.hh.ru/areas` (или `areas` в ответе `GET /api/v1/...`) |
| `professional_role` | ID профессиональной роли (`96` — программист) |
| `experience` | опыт: `noExperience`, `between1And3`, `between3And6`, `moreThan6` |
| `schedule` | график: `remote`, `fullDay`, `flexible`, `shift`, `flyInFlyOut` |
| `employment` | занятость: `full`, `part`, `project`, `probation`, `volunteer` |
| `salary` / `only_with_salary` | минимальная зарплата и фильтр «только с зарплатой» |
| `period` | за сколько последних дней искать (1, 3, 7, 30) |
| `page_size` / `max_pages` | размер страницы и предел пагинации за один проход |
| `order_by` | сортировка: `publication_time`, `salary_desc`, `relevance` |

Сколько откликов делать, задаётся тремя разными полями:

| Поле | Где | Смысл |
|---|---|---|
| `target_applications` | `searches[]` | сколько подходящих вакансий собрать из этого поиска за один проход |
| `target_successful` | `job.action` рассылки | цель по **подтверждённым** откликам: рассылка переходит к следующему маршруту (`route`) или останавливается, когда их набралось столько |
| `daily_limit` | `profile.applications` | жёсткий дневной потолок отправок на профиль/платформу |
| `max_in_flight` | `job.action` рассылки | сколько откликов одновременно «в работе»: рассылка ставит следующий, только когда незавершённых меньше лимита; `1` — строго по одному |

Полный список ключей с типами и значениями — в
[`reference/configuration.md`](reference/configuration.md), готовые куски
конфига — в [`reference/recipes.md`](reference/recipes.md).

### Как работает рассылка

Рассылка — это конвейер, а не пачка одновременных запросов:

1. она листает выдачу активного поиска и создаёт задачи отклика на подходящие
   вакансии;
2. одновременно «в работе» может быть не больше `max_in_flight` откликов:
   пока незавершённых столько же, новые не создаются;
3. завершённые отклики (отправлен, пропущен, ошибка) освобождают место, и
   рассылка берёт следующие вакансии;
4. когда выдача поиска исчерпана, она переходит к следующему маршруту
   `routes`; когда набрано `target_successful` подтверждённых откликов —
   останавливается.

`max_in_flight: 1` — строго последовательная отправка. Даже при большем
значении параллельность ограничивают `submit_jitter`, per-profile lock
(browser-отправки одного профиля не идут одновременно) и лимиты платформы.

## 2. Один раз войдите в HH

Вход сохраняется в browser storage state профиля (`state_file`) и переживает
перезапуск. Выберите любой способ:

- **CLI**: `job-agent auth login --profile main --state-output ./data/profiles/main.json`
  (попросит e-mail и код). Команды используют собранные бинарники: один раз
  выполните `make build` и `export PATH="$PWD/dist:$PATH"` (либо пишите
  `./dist/job-agent` явно);
- **Dashboard** (опционально): секция «Вход в HH» → профиль `main` → «Начать
  вход»;
- **Импорт готовой сессии**: `job-agent auth import --source export.json --state-output ./data/profiles/main.json --force`.

Если у вас OAuth-доступ к API HH, браузерный вход не обязателен: укажите
`credentials_ref` в профиле, и API-операции пойдут без cookies.

При Docker-запуске backend не публикует порт на хост: для CLI-входа добавьте
флаг `--api http://127.0.0.1:8081` (dashboard proxy) либо войдите через сам
dashboard.

> Дальше ничего нажимать не нужно: cron внутри сервиса сам выполнит поиск,
> поднятие резюме и отклики по расписанию из конфига.

## 3. Запуск

### Docker Compose

```sh
export JOB_AGENT_API_TOKEN="$(openssl rand -hex 32)"
JOB_AGENT_CONFIG_DIR=./deploy JOB_AGENT_CONFIG_NAME=config.json JOB_AGENT_DATA_DIR=./data \
  docker compose --profile browser up -d --build
```

Профиль `browser` добавляет worker для входа и browser-only операций. Без него
сервис тоже работает, но login и анкеты будут недоступны.

`JOB_AGENT_API_TOKEN` — необязательный общий секрет (не пользовательская
авторизация). В Compose он рекомендован, потому что backend доступен
контейнерам приватной сети, а dashboard подставляет токен server-side. Можно
работать и без него: не задавайте переменную и уберите `api_token_env` из
конфига — тогда API не требует заголовка (dashboard продолжит работать).
Подробнее — в
[справочнике конфигурации](reference/configuration.md#зачем-bearer-токен-и-как-работать-без-него).

### Локально без Docker

```sh
make build
export PATH="$PWD/dist:$PATH"   # job-agent* доступны без префикса
job-agent-migrate -config ./deploy/config.json up
job-agent ./deploy/config.json
```

Для локального запуска токен не нужен: уберите `api_token_env` из конфига и не
задавайте `JOB_AGENT_API_TOKEN` — backend будет слушать только loopback
(`127.0.0.1:8080`). Токен обязателен при доступе по сети (WireGuard/туннель)
и рекомендован в Compose.

Dashboard (опционально) — `./dist/job-agent-dashboard`. Для browser-операций
запустите worker: `cd browser-worker && npm ci && npm run build && npm start`.

## 4. Несколько профилей HH

Каждый аккаунт — отдельный профиль со своим тегом, резюме, state-файлом и
политикой. Вакансия хранится в базе один раз, а отклик — отдельно для пары
профиль/вакансия, поэтому один и тот же поиск может распределяться между
аккаунтами без дублей.

```json
{
  "adapters": [{"tag": "hh-main", "type": "hh"}],
  "profiles": [
    {
      "tag": "backend",
      "adapter": "hh-main",
      "resume": "resume-id-backend",
      "state_file": "/data/profiles/backend.json",
      "applications": {
        "mode": "submit",
        "daily_limit": 30,
        "submit_jitter": {"min": "15s", "max": "30s"},
        "message_template_file": "messages/backend.json",
        "timezone": "Europe/Moscow"
      }
    },
    {
      "tag": "golang",
      "adapter": "hh-main",
      "resume": "resume-id-golang",
      "state_file": "/data/profiles/golang.json",
      "applications": {
        "mode": "dry_run",
        "message_template_file": "messages/backend.json",
        "timezone": "Europe/Moscow"
      }
    }
  ],
  "searches": [
    {
      "tag": "golang-global",
      "adapter": "hh-main",
      "profiles": ["backend", "golang"],
      "query": {"source": "global", "text": "Golang developer", "area": ["1"]}
    }
  ],
  "jobs": [
    {
      "tag": "daily-applications",
      "triggers": [{"type": "cron", "expression": "30 9 * * *", "timezone": "Europe/Moscow"}],
      "action": {
        "type": "application.campaign",
        "profiles": ["backend", "golang"],
        "routes": ["golang-global"],
        "target_successful": 40,
        "max_in_flight": 2
      }
    }
  ]
}
```

Правила:

- `tag` профиля выбираете вы — это просто имя объекта для ссылок;
- у каждого профиля собственные `resume`, `state_file`, `daily_limit` и
  `submit_jitter`; один аккаунт может быть в `submit`, другой в `dry_run`;
- `searches[].profiles` и `job.action.profiles` перечисляют, какие профили
  участвуют; отклики не пересекаются благодаря ключу профиль/вакансия;
- вход выполняется один раз для каждого профиля.

## 5. От dry-run к реальным откликам

Пока `"mode": "dry_run"`, сервис делает всё, кроме отправки: находит
вакансии, готовит письма и показывает, какие отклики были бы отправлены.
Проверьте тексты писем и фильтры, после чего включите submit:

```json
"applications": {
  "mode": "submit",
  "daily_limit": 30,
  "submit_jitter": {"min": "15s", "max": "30s"}
}
```

`daily_limit` — потолок откликов в день на профиль/платформу,
`submit_jitter` — пауза между отправками; это темп, который сервис соблюдает
сам вместе с лимитами платформы. Если вакансия требует анкету, отклик
остановится до вашего ответа: заполните форму в разделе «Проверки и опросники»
(dashboard) или ответьте на вопрос в чате — отправка продолжится автоматически.

## 6. Наблюдение и обслуживание

Без dashboard всё доступно через CLI и API. Команды выполняются из каталога
репозитория после `make build` (`dist/` в PATH):

```sh
export PATH="$PWD/dist:$PATH"

job-agent-check ./deploy/config.json               # ready | degraded | blocked
curl -s -H "Authorization: Bearer $JOB_AGENT_API_TOKEN" "http://127.0.0.1:8080/api/v1/applications?limit=20"
curl -s -X POST -H "Authorization: Bearer $JOB_AGENT_API_TOKEN" -H "Idempotency-Key: $(uuidgen)" \
  "http://127.0.0.1:8080/api/v1/jobs/daily-applications/runs"
job-agent db backup --config ./deploy/config.json
job-agent db restore --config ./deploy/config.json --input <backup> --force
```

Команды требуют собранных бинарников (`make build`, `dist/` в PATH).

Upgrade и restore выполняются на остановленном сервисе, после backup.
Типовые сбои и восстановление — в [`runbook.md`](runbook.md).
