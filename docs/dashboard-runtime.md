# Dashboard, контейнерный runtime и общий browser service

## Принятое устройство

Dashboard — опциональный клиент control plane, а не второй источник бизнес-логики. Он читает REST API backend и создаёт те же durable tasks, что CLI или будущий MCP-клиент. Прямого доступа к SQLite, адаптерам и браузеру у него нет.

```text
WireGuard client
    |
    | http://<точный tunnel IP>:8081
    v
dashboard (static UI + reverse proxy)
    |
    | private Compose network
    v
job-agent API -> workflows -> durable task table -> typed consumers
                                                -> platform API
                                                -> browser service (later)
```

В одном `compose.yaml` описаны ровно два сервиса: `job-agent` и `dashboard`. Они запускаются разными командами из одного runtime image с тремя статическими Go-бинарниками; `job-agent -migrate-up` применяет ожидающие миграции перед открытием хранилища, а отдельный `job-agent-migrate` остаётся в image для ручного управления схемой. Полный `docker compose up` включает UI; запуск только `job-agent` оставляет dashboard выключенным без дополнительного Compose profile или override-файла. Backend имеет только `expose`, но не `ports`: с хоста и внешней сети он напрямую не публикуется. В контейнерном конфиге используется `server.exposure: private`; обычное значение по умолчанию `loopback` продолжает запрещать non-loopback bind.

## Доступ через WireGuard/WireGuard

Docker публикует порт на IP хоста, а не на имени интерфейса. Поэтому стабильный контракт deployment — `JOB_AGENT_DASHBOARD_BIND_IP=<адрес wg-интерфейса>`. Например, если у `wg0` адрес `10.66.66.1`, dashboard публикуется только как `10.66.66.1:8081:8081`.

Имя интерфейса намеренно не передаётся приложению: контейнер не обязан видеть host network namespace, а `network_mode: host` ухудшил бы изоляцию и переносимость. Разрешение `wg0 -> IP` при динамическом адресе должно делать узкое средство развёртывания перед `docker compose up`. Значение `0.0.0.0` использовать нельзя: API пока не имеет пользовательской аутентификации, а WireGuard является границей доступа только при bind на его точный адрес и корректном firewall.

## Один браузер, несколько профилей

Профиль — учётная запись и её состояние, worker — consumer задачи. Эти понятия не связаны один-к-одному. Целевой browser service держит один процесс Chromium и отдельный `BrowserContext` на каждый профиль:

```text
browser service
|- Chromium process
|- profile-a: BrowserContext + serial mutation lane
|- profile-b: BrowserContext + serial mutation lane
`- bounded page pool
```

Go backend отправляет узкие операции (`application.submit`, `resume.touch`, `conversation.sync`), а не произвольный JavaScript или команды shell. Browser service проверяет capability, выбирает контекст и сериализует изменяющие операции одного профиля. Это предотвращает гонки cookies, навигации и формы, но позволяет разным профилям работать параллельно в пределах общего лимита страниц.

Существующие HH browser transports пока не запускают Chromium: они читают sanitised Playwright storage state и выполняют HTTP-запросы с cookies. Поэтому вынос реального Chromium нужен только для DOM-only операций, интерактивной авторизации, challenge и форм, которых нет в API/HTTP-контракте.

## Очереди и приоритеты

Приоритет и отдельная очередь решают разные задачи:

- `priority` определяет порядок claim внутри одной полосы исполнения;
- отдельный consumer/pool изолирует бюджет ресурса и не даёт тяжёлому типу задач занять весь runtime;
- per-profile lock защищает одну внешнюю сессию от конфликтующих мутаций.

Все задачи лежат в одной durable SQLite-таблице, а consumer’ы claim’ят только
свой `TaskType`. Числовой `priority` хранится вместе с задачей и сортирует
доступные элементы по `priority DESC`, затем по `available_at`, `created_at` и
ID. Он задаётся у декларативного job, переживает retry/restart и передаётся
дочерним campaign/application и search-page tasks. Равный priority сохраняет
FIFO; уже взятая задача не вытесняется. Dashboard показывает priority отдельной
колонкой.

Физически дробить очередь сейчас не требуется. `profile_state.apply` и
`resume.touch` уже проходят через общую process-local mutation lane по профилю;
разные профили не блокируют друг друга. Поскольку Compose запускает один
backend, этого достаточно для текущего deployment. До запуска mutating workers
в нескольких репликах lane должна стать распределённой lease/lock в durable
storage. Для будущего browser service дополнительно нужен общий ограничитель
страниц. Если внутри одного task type появится постоянный поток одного профиля,
следующим отдельным механизмом станет profile-aware round-robin/aging: priority
сам по себе не гарантирует fairness и не должен притворяться отдельной очередью.

## Реализованный UI-срез

- агрегированные показатели runtime;
- идемпотентная сводка подтверждённых действий профиля и последние read-only
  снимки показов/просмотров/приглашений HH без выдуманного activity-score;
- состояния очереди и откликов;
- список диалогов и загрузка сообщений;
- постановка `conversation.send` и `conversation.mark_read` в durable queue;
- liveness dashboard и proxy к backend health/readiness.

UI вызывает настоящие backend endpoints. HH browser conversation transport уже
поддерживает sync, отправку и mark-read; мутации дополнительно защищены
profile-policy `conversations.allow_send` и `allow_mark_read`.

## Следующие срезы

1. Единый auth control plane для CLI/dashboard, credential sinks и Kitty/Sixel
   challenges — [`next-auth-control-plane.md`](next-auth-control-plane.md).
2. Фильтры и пагинация dashboard, подробности campaign/application и task retry/cancel.
3. Profile-aware fairness/aging внутри одного task type без нарушения priority.
4. Узкий browser RPC, один Chromium, контексты профилей и bounded page pool.
5. SSE для обновлений вместо периодического polling и для auth challenges.
