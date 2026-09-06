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

Dashboard запускается Compose-профилем `dashboard`, поэтому без профиля сервис не создаётся. Backend имеет только `expose`, но не `ports`: с хоста и внешней сети он напрямую не публикуется. В контейнерном конфиге используется `server.exposure: private`; обычное значение по умолчанию `loopback` продолжает запрещать non-loopback bind.

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

В текущем срезе все задачи лежат в одной durable SQLite-таблице, а consumer’ы claim’ят только свой `TaskType`. Физически дробить очередь сейчас не требуется. Следующий шаг — добавить priority в task schema и сортировку claim по `priority DESC, available_at, created_at`, сохранив отдельные consumer’ы для search, applications, resume и conversations. Для browser service поверх этого нужен общий ограничитель страниц и последовательная mutation lane на профиль.

## Реализованный UI-срез

- агрегированные показатели runtime;
- состояния очереди и откликов;
- список диалогов и загрузка сообщений;
- постановка `conversation.send` и `conversation.mark_read` в durable queue;
- liveness dashboard и proxy к backend health/readiness.

UI уже вызывает настоящие backend endpoints, однако живой HH conversation transport пока возвращает `unsupported`. Поэтому задача из UI сохранится и будет видна в очереди, но реальная отправка/mark-read в HH появится после реализации transport.

## Следующие срезы

1. API authentication/session для доступа не только из доверенного туннеля.
2. Фильтры и пагинация dashboard, подробности campaign/application и task retry/cancel.
3. Реальный HH conversation transport и sync входящих сообщений.
4. Priority в persistent task schema и fairness между профилями.
5. Узкий browser RPC, один Chromium, контексты профилей и bounded page pool.
6. SSE для обновлений вместо периодического polling.
