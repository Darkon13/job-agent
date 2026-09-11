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
- Терминальные рендеры auth-challenge: Kitty graphics, Sixel с web-палитрой,
  Unicode half-block preview и приватный PNG-файл как fallback.

Это development-версия, не production-релиз `v1`. Миграции и GC на рабочей базе
в рамках разработки не запускались; режим отправки откликов не менялся.
