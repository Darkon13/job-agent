# Изменения

## 0.3.0-dev

- Локальное удаление выбранных откликов через durable tasks; отдельный GC job
  с настраиваемым сроком и защитой приглашений.
- Сохранение переписки, квот, pacing, истории кампаний и dedup после очистки;
  миграции SQLite 19–20 с защитой от разрушительного downgrade.
- Поиск по всей базе, Unicode-фильтр компаний, категории, сортировка и страницы
  applications API; select→action в dashboard с результатом по каждому объекту.
- Отдельные широкие таблицы jobs/очереди, фильтры чатов, автоматическое чтение
  при открытии и блокировка повторного «Прочитать всё».
- Наблюдатель статусов HH через OAuth/API. Для browser-cookie профилей
  автоматический GC пока недоступен; подробности в `application-cleanup.md`.
- Временная подстройка резюме перед откликом: durable saga с baseline/target
  snapshot, apply и restore через profile state; включается
  `applications.tailoring.skills`, модель выбирает навыки с fallback на
  детерминированную политику; активная фаза, skill diff и recovery показываются
  в dashboard; подробности в `application-resume-tailoring.md`.
- Credential record в пакете `credentials`: чтение JSON и dotenv
  (`file:`/`dotenv-file:`), атомарная запись `0600` без symlink и случайной
  перезаписи; HH adapter читает refresh-совместимую запись.
- Auth control plane: durable auth session с revision CAS, эфемерный в памяти
  challenge store с TTL и HTTP endpoints для identifier/OTP/password/captcha,
  отмены и чтения redacted-состояния.
- HH search: `similar_resume`, `similar_vacancy` и `related_vacancy` через API и
  `similar_resume` через browser `/search/vacancy?resume=`; у source появилась
  матрица поддерживаемых полей.
- `resume.publish` через applicant API: job action, durable task, worker и
  нормализация ранней публикации в `rate_limited` с `next_publish_at`.
- Browser worker на Playwright с узким HTTP RPC: контексты профилей,
  storage state, page-примитивы, Bearer-токен и Compose-профиль `browser`.
- Интерактивный HH login через браузер: email → OTP/captcha, browser storage
  state как артефакт сессии (миграция 22) и CLI `job-agent auth login|status`.
- Извлечение вопросов vacancy test из popup initial state в
  `core.Questionnaire` (вопросы, варианты, open-text и code-задачи).
- Отправка open-text ответов vacancy test через popup-форму с проверкой
  `_xsrf`/`uidPk`/`guid`/`startTime` и обязательным read-back результата.
- Worker `test.capture`: наблюдаемые вопросы vacancy test попадают в
  прогрессивный тестовый каталог монотонным upsert, без запуска попытки.
- Worker `questionnaire.answer`: отправка уже resolved ответов через
  browser write transport (входные ответы не догадываются в worker-е).
- Worker `test.complete` и миграция 23: монотонный результат vacancy test по
  профилю, где `passed` не понижается поздним `failed`.
- Цепочка vacancy test: blocked application ставит идемпотентный
  `test.capture`; `submitted`/`passed` attempt снимает статический
  `has_test`-блок, и повторный submit продолжает pipeline.
- Known-answer routing: `answer_sets` в конфиге, vacancy-`AnswerBlock` и
  `UncoveredQuestions`; полное покрытие ставит `questionnaire.answer`,
  неполное создаёт детерминированную review session без submit, успешный
  submit фиксируется как `test.complete(submitted)`.
- Worker `review.answer` и миграция 24: runtime questionnaire хранится в
  review session; человеческий выбор записывается с CAS, при полном покрытии
  цепочка возобновляет `questionnaire.answer`, иначе выдаётся следующий
  missing prompt.
- Review API и CLI: `GET /api/v1/review-sessions/{id}` отдаёт текущий prompt,
  `POST .../answers` принимает выбор с Idempotency-Key и ставит durable
  `review.answer`; `job-agent review show|answer` работает как API-клиент.
- Append-only ревизии answer blocks (миграция 25): ответ человека сохраняется
  ревизией и переиспользуется в следующих capture через композит
  `ReviewedVacancyAnswers`; повтор идентичного ответа ревизию не создаёт.
- Bearer-токен API: `server.api_token_env` включает проверку заголовка
  `Authorization` (health-пробы открыты), не-loopback bind разрешён только с
  токеном; dashboard проксирует запросы со своим `JOB_AGENT_API_TOKEN`.
- Single-replica guard (миграция 26): startup берёт exclusive runtime lease в
  SQLite с renew и takeover после падения; второй живой инстанс завершается
  с понятной ошибкой.
- Config builder: top-level `include` с glob, depth-first merge, cycle/depth
  guard и ошибками с путём к файлу; файловые ссылки внутри include резолвятся
  относительно объявившего их файла.
- Терминальные рендеры auth-challenge: Kitty graphics, Sixel с web-палитрой,
  Unicode half-block preview и приватный PNG-файл как fallback.

Это development-версия, не production-релиз `v1`. Миграции и GC на рабочей базе
в рамках разработки не запускались; режим отправки откликов не менялся.
