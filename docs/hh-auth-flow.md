# HH: интерактивная и удалённая авторизация

Документ фиксирует исследованный browser login flow и целевой контракт
HH-адаптера. Значения cookies, tokens, email и телефонов здесь не сохраняются.

## Цель

Пользователь не должен вручную экспортировать cookies. Сервис сам открывает
страницу входа, вводит email/телефон и приостанавливает auth session только в тех
местах, где требуется действие пользователя:

- ввод одноразового кода из SMS/email;
- captcha;
- подтверждение неожиданного сценария;
- при необходимости — пароль.

Ожидание не блокирует весь процесс приложения. Auth session сохраняет состояние,
а CLI/UI отправляет ответ отдельной командой или API-вызовом.

## Два результата авторизации

Гибридному HH-адаптеру нужны независимые артефакты:

1. OAuth access/refresh tokens для публичного API.
2. Browser storage state/cookies для web-only действий.

Успешный вход считается полностью завершённым после получения и безопасного
сохранения обоих результатов. Browser cookies нельзя использовать как замену
OAuth token, и наоборот.

## Целевая state machine

```text
created
-> browser_starting
-> identifier_required
-> challenge_requesting
   |- code_required
   |- password_required
   |- captcha_required
   `- failed
-> challenge_submitted
-> oauth_redirect_received
-> token_exchanging
-> persisting_session
-> authenticated
```

Любое ожидающее состояние содержит deadline и public challenge metadata. Secret
значения не попадают в events и logs.

## Режимы

### Assisted code login

Основной пользовательский сценарий:

1. Пользователь создаёт auth session и передаёт email или телефон.
2. Browser worker открывает applicant login/OAuth page.
3. Worker выбирает тип credential и вводит identifier.
4. HH отправляет одноразовый код.
5. Backend публикует `auth.code_required` с auth session ID и masked destination,
   если HH его показывает.
6. Пользователь вводит код через CLI, localhost UI или Telegram control channel.
7. Worker вводит код и ждёт OAuth redirect/успешную сессию.
8. Adapter обменивает authorization code на OAuth tokens и сохраняет browser
   storage state.

Концептуальный интерфейс:

```text
POST /auth/sessions
POST /auth/sessions/{id}/identifier
POST /auth/sessions/{id}/code
POST /auth/sessions/{id}/captcha
GET  /auth/sessions/{id}
DELETE /auth/sessions/{id}
```

CLI является клиентом этого API, а не отдельной реализацией auth flow.

### Visible/manual login

Для отладки, captcha и изменившихся страниц browser worker может показать окно
через VNC/local desktop. После ручного завершения worker всё равно сохраняет те
же OAuth/browser artifacts и возвращает обычный `authenticated` result.

### Password login

Поддерживается как optional branch. Пароль не сохраняется по умолчанию. Если
пользователь включает secret storage, он хранится отдельно от config.json.
Одноразовый код остаётся предпочтительным сценарием.

## Наблюдаемая форма на 2026-07-18

Applicant login URL:

```text
/account/login?role=applicant&backurl=...&hhtmFrom=...
```

Первый экран выбирает роль `APPLICANT`/`EMPLOYER`. После выбора applicant
доступны credential types `PHONE` и `EMAIL`.

Наблюдаемые стабильные атрибуты:

| Элемент | `data-qa` |
|---|---|
| Phone credential | `credential-type-PHONE` с возможным suffix `checked` |
| Email credential | `credential-type-EMAIL` с возможным suffix `checked` |
| Country calling code | `magritte-phone-input-calling-code-input` |
| National phone number | `magritte-phone-input-national-number-input` |
| Email | `applicant-login-input-email`, form name `username` |
| Continue | `submit-button` |
| Password branch | `expand-login-by-password` |
| Social methods | `account-login-social-show-more` |
| OTP input | `magritte-pincode-input-field`, `autocomplete=one-time-code` |
| Resend OTP | `applicant-login-button-code-sender` |
| Change credential | `change-credential-button` |

Suffix `checked` в `data-qa` нельзя считать частью точного селектора. Использовать
prefix/role locator и проверять фактический `checked` state.

Старый reference implementation использует `login-input-username`,
`account-login-code-input` и `magritte-pincode-input-field`. Первый селектор уже
не соответствует текущему начальному экрану. Последний присутствует в текущем
OTP flow; `account-login-code-input` не нужен как основной locator.

### Генерация OTP challenge

После отправки email браузер вызвал:

```text
POST /account/otp_generate
Content-Type: multipart/form-data
Accept: application/json
```

Наблюдаемые multipart-поля (значения намеренно не фиксируются):

| Поле | Назначение |
|---|---|
| `login` | Переданный identifier |
| `otpType` | Тип OTP/канала |
| `operationType` | Операция авторизации |
| `isSignupPage` | Контекст login/signup |
| `authScenario` | Сценарий авторизации |
| `loginTrustFlags` | Флаги доверия к login |
| `captchaText` | Ответ captcha, пустой вне captcha branch |

Запрос также содержит XSRF и внутренние anti-bot/tracing headers. Adapter не
должен конструировать их из захардкоженных значений: web-запрос выполняет сама
страница в browser context.

Ответ — JSON со следующими группами данных:

- `success`, `state`, `key`, `notificationType`, `otpSendState`;
- `codeLength`, `expectedOperationType`;
- `nextSendTime`, `nextConfirmTime`, `expirationTime`;
- `recaptcha` и `hhcaptcha` с captcha status/metadata;
- `accountType`, `isMultiAccount`;
- `otp` с auth/account metadata, `secondsUntilNextSend` и `backurl`.

В наблюдаемом email flow `codeLength` соответствует четырём цифрам. На экране
есть resend с таймером и возврат к смене identifier. У ответа генерации challenge
не было `Set-Cookie`; это не означает, что cookies не изменятся на следующих
шагах.

### Подтверждение OTP

После ввода корректного кода браузер вызвал:

```text
POST /account/login/by_code
Content-Type: multipart/form-data
```

Наблюдаемые multipart-поля:

| Поле | Назначение |
|---|---|
| `username` | Identifier аккаунта |
| `code` | Одноразовый код |
| `operationType` | Операция авторизации |
| `backurl` | Целевая страница после входа |
| `isApplicantSignup` | Контекст applicant login/signup |
| `remember` | Режим сохранения входа |
| `loginTrustFlags` | Флаги доверия к login |
| `authScenario` | Сценарий авторизации |

В наблюдаемом успешном запросе сервер ответил `200` с пустым body, после чего
страница перешла на `/`. Поэтому успешность нельзя определять только по JSON
ответу: worker ждёт navigation и проверяет authenticated markers. На главной
исчезли login/OTP controls и появились элементы профиля и резюме.

Этот flow создаёт web session. Сам по себе он не доказывает получение OAuth
access/refresh tokens для публичного API: OAuth authorization и token exchange
остаются отдельным этапом.

### Image captcha challenge

Reference implementation распознаёт captcha branch по двум DOM-элементам:

| Элемент | Selector |
|---|---|
| Изображение | `img[data-qa="account-captcha-picture"]` |
| Ответ | `input[data-qa="account-captcha-input"]` |

Изображение берётся через screenshot самого DOM-элемента, поэтому browser
context автоматически сохраняет нужные cookies, headers и anti-bot state. После
ручного ввода текст отправляется через captcha input; затем state machine снова
определяет текущий экран, а не предполагает конкретный redirect.

В Job Agent captcha является отдельным short-lived challenge:

```text
browser/API adapter
-> captcha.required event
-> challenge blob (image/png, TTL)
-> CLI / localhost UI / optional notification client
-> POST /auth/sessions/{id}/captcha
-> browser worker submits answer
-> challenge blob and plaintext answer are deleted
```

Broker event содержит только `challenge_id`, profile/platform IDs, expiration и
metadata изображения. PNG не следует класть прямо в общий event payload: клиент
получает его из временного защищённого blob store по challenge ID. Текст ответа,
изображение и URL с captcha state не попадают в логи.

CLI выбирает renderer по возможностям терминала:

- Kitty graphics protocol: передать PNG как base64 payload;
- Sixel: декодировать PNG, уменьшить палитру и вывести Sixel raster;
- fallback: показать localhost UI/VNC или безопасный временный файл.

Renderer — только клиент auth API. Он не должен владеть Playwright page или
реализовывать собственный login flow.

Captcha может появиться не только при входе. Публичный API описывает ошибку
`captcha_required` с `captcha_url` и/или `fallback_url`. К `captcha_url` нужно
добавить абсолютный `backurl`, открыть его в browser context того же профиля,
пройти ручной challenge и повторить исходную API-команду с идемпотентностью и
ограниченным числом попыток. Новый пустой context для этого использовать нельзя:
он теряет связь с profile session.

Надёжного тестового переключателя для принудительного вызова captcha нет. Не
следует провоцировать anti-bot защиту серией неверных входов или частых запросов.
Ветку проверяем при естественном challenge либо изолированным fixture/page в
тестах browser worker.

## Устойчивость browser flow

Auth worker не должен быть линейным скриптом из фиксированного списка кликов.
После каждого перехода он определяет текущее состояние по набору признаков:

- URL и navigation event;
- наличие credential/code/password/captcha controls;
- OAuth redirect;
- authenticated page marker;
- structured validation alert.

Locators выбираются в порядке:

1. стабильный `data-qa`;
2. form field name/type;
3. accessible role/label;
4. текст только как диагностический fallback.

Если страница неизвестна, auth session переходит в `manual_required`, окно
остаётся открытым, а не завершается неявной ошибкой.

## OAuth redirect

Reference implementation открывает OAuth authorize URL, перехватывает custom
scheme redirect и извлекает authorization code. Job Agent использует собственное
зарегистрированное HH application credentials и redirect URI. Нельзя копировать
Android client ID/secret из стороннего проекта.

Поддерживаемые варианты callback transport:

- loopback callback внутри browser worker;
- перехват navigation/request custom scheme;
- обычный HTTPS callback в backend при доступной конфигурации.

Authorization code короткоживущий и не записывается в логи.

## Browser storage

Для каждого platform profile сохраняется отдельный Playwright storage state или
persistent context directory. Файл:

- находится в persistent volume;
- имеет права только для пользователя сервиса;
- исключён из Git;
- не возвращается через UI/MCP;
- заменяется атомарно после успешной авторизации;
- удаляется/инвалидируется при logout.

До авторизации были замечены служебные cookie names для region/device/analytics,
XSRF и anti-bot state. Наличие cookie до входа не доказывает, что он нужен для
авторизованной сессии. Нужный минимальный набор определяется diff до/после входа
по именам, domain/path, expiry, HttpOnly/Secure/SameSite — без публикации values.

После успешного web login среди cookies домена HH наблюдались потенциально
auth-related names `hhtoken`, `hhul`, `hhuid`, `crypted_hhuid`, `crypted_id` и
`hhrole`. Это диагностический список, а не минимальный контракт: без чистого
pre-login context нельзя утверждать, какие именно cookies созданы данным шагом.
`hhtoken` и `hhul` были недоступны JavaScript (`HttpOnly`), поэтому сохранение
только `document.cookie` заведомо недостаточно. Нужен Playwright `storageState`
или persistent context.

Local/session storage после входа в основном содержал UI, telemetry, chat и
anti-bot keys; очевидного OAuth token key не обнаружено. Значения storage keys
не исследуются и не логируются.

Из браузера сначала экспортируется полный storage state: HH может менять состав
сессии, а ручной allowlist по именам cookies быстро устаревает. Перед помещением
в профиль Job Agent export обязательно атомарно очищается командой
`job-agent-browser-state sanitize`: сохраняются все записи доменов `hh.ru` и
`hhcdn.ru`, но удаляются cookies и origins остальных сайтов общего Playwright
профиля. Имена отдельных HH cookies не становятся контрактом.

### Ограничение новых applicant OAuth integrations

На 2026-09-06 публичная OpenAPI-документация всё ещё описывала applicant OAuth,
но форма регистрации нового приложения в `dev.hh.ru/admin` сообщала о
прекращении поддержки API для соискателей с 15 декабря 2025 года. Это разные
сигналы: существующий токен по-прежнему поддерживается кодом, но возможность
зарегистрировать новое соискательское приложение не считается доступной.

Текущий путь локального запуска поэтому не ждёт OAuth client credentials:

1. пользователь входит через общий Playwright/VNC browser;
2. storage state экспортируется и санитизируется до HH-доменов;
3. server-rendered поиск и полная вакансия читаются только GET-запросами;
4. application доходит до `dry_run`, а submit transport не регистрируется;
5. browser-only поднятие резюме остаётся отдельной явно настроенной job.

Анонимный public vacancy search не заменяет этот путь: актуальная документация
предупреждает о captcha без access token, а живая проверка с этой машины получила
`403` уже на первом запросе.

### Импорт готового API token

Первый реализованный auth-срез не проводит интерактивный OAuth flow. Профиль
ссылается через `credentials_ref` на отдельный JSON-файл с `access_token`,
доступный только владельцу (`0600` или строже). При старте HH read client
выполняет `GET /me` под per-profile lock. Валидный applicant token оставляет
профиль включённым; истёкший, отозванный или неверный token переводит его в
`auth_required` и отключает workers этого профиля без бесконечного retry.

`refresh_token` и автоматический обмен пока не реализованы. Тот же per-profile
lock станет границей refresh, чтобы два конкурентных запроса не обновляли одну
сессию одновременно.

Следующий вертикальный срез — единая backend auth session, безопасная запись
access/refresh token в JSON или dotenv, dashboard-клиент и вывод captcha/QR
через Kitty graphics protocol либо Sixel. CLI syntax, разделение stdout/TTY и
критерии готовности зафиксированы в
[`next-auth-control-plane.md`](next-auth-control-plane.md).

## Security

- identifier маскируется в событиях и логах;
- password, OTP, OAuth code, tokens и cookie values никогда не логируются;
- OTP принимается только для активной auth session и сразу очищается из памяти;
- количество попыток и resend ограничиваются;
- challenge timeout приводит к `expired`, а не бесконечному ожиданию;
- captcha всегда переводится в manual input, без автоматического обхода;
- один profile не может иметь две конкурирующие auth sessions;
- после входа выполняется browser session probe; API `/me` вызывается только
  при наличии отдельного OAuth token.

## Что ещё проверить в живом потоке

- поведение resend после истечения таймера;
- captcha branch;
- password branch после валидного identifier;
- условия выдачи новых applicant client credentials и только после этого OAuth
  redirect URI/token exchange;
- cookie/storage diff в новом чистом context до и после входа;
- признаки истёкшей browser session;
- logout и повторную авторизацию;
- поведение при уже существующем аккаунте с включённой 2FA.
