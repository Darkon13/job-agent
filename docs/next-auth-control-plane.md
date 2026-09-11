# Следующая задача: auth control plane и вывод credentials

Статус: `in_progress`. Credential record и JSON/dotenv reader/writer, persistent
auth session, эфемерный challenge store, платформо-нейтральный auth service и
HTTP endpoints реализованы; HH login driver, terminal/dashboard presenters и SSE
ещё не начаты. Endpoints регистрируются вместе с первым driver-ом.

Живой HH login, OAuth exchange, captcha и обновление токенов должны быть одним
backend workflow. CLI и dashboard являются клиентами одной auth session, а не
двумя независимыми реализациями входа. Исследованный HH flow описан в
[`hh-auth-flow.md`](hh-auth-flow.md).

## HTTP API

```text
POST /api/v1/auth/sessions
GET  /api/v1/auth/sessions/{session_id}
POST /api/v1/auth/sessions/{session_id}/inputs
POST /api/v1/auth/sessions/{session_id}/cancel
GET  /api/v1/auth/sessions/{session_id}/challenge
```

`inputs` принимает `{"kind":"identifier|otp|password|captcha","value":"..."}`;
`challenge` отдаёт PNG или URL как raw body с media type и `no-store`.
Отправка лишнего шага возвращает `409`, незнакомая сессия — `404`. Сессия в
ответе redacted: токены, коды и изображения в JSON не попадают.

## Credential storage

Пакет `credentials/` читает и пишет канонический `Record` с access/refresh
token, expiry, token type, scopes, platform/profile и revision. Поддерживаемые
`credentials_ref`:

- `file:/path/credentials.json` — основной JSON-формат;
- `dotenv-file:/path/credentials.env` — dotenv с `HH_ACCESS_TOKEN`,
  `HH_REFRESH_TOKEN`, `HH_EXPIRES_AT`;
- bare path без схемы сохранён для совместимости и трактуется как JSON.

Reader принимает только regular file с правами без group/other и ограничивает
размер записи. Writer пишет атомарно через temp-файл и rename, выставляет
`0600`, не следует по symlink и не перезаписывает существующий secret без
явного `force`. Секреты не попадают в сообщения ошибок.

## Пользовательские сценарии

Безопасный основной сценарий сам записывает secret-файл:

```sh
job-agent auth login \
  --profile primary \
  --presenter auto \
  --credential-format json \
  --credential-output ./data/credentials/hh-primary.json
```

Unix pipeline также поддерживается, но вывод секрета в stdout должен быть
разрешён явно:

```sh
job-agent auth login \
  --profile primary \
  --presenter auto \
  --credential-format dotenv \
  --credential-output - > ./data/credentials/hh-primary.env
```

Без `--credential-output -` access/refresh token никогда не печатаются в
stdout. При прямой записи CLI создаёт файл атомарно с правами `0600`, не следует
по symlink и не перезаписывает существующий secret без отдельного `--force`.
Progress и redacted status идут в stderr; интерактивный prompt и изображения —
в controlling terminal (`/dev/tty`). Поэтому перенаправление stdout не смешает
dotenv с подсказками, captcha или escape-последовательностями.

Канонический credential record содержит как минимум access token, refresh
token, expiry, token type, scopes, platform/profile и revision. JSON остаётся
основным машинным форматом. Dotenv — поддерживаемое представление:

```dotenv
HH_ACCESS_TOKEN='...'
HH_REFRESH_TOKEN='...'
HH_EXPIRES_AT='...'
```

Значения кодируются безопасно для dotenv и никогда не попадают в argv, логи,
audit payload или shell history. Для использования dotenv как постоянного
источника config loader получает отдельную схему
`credentials_ref: dotenv-file:/run/secrets/hh-primary.env`; существующий JSON
ref остаётся `file:/run/secrets/hh-primary.json`. Если runtime должен вращать
refresh token, credential ref обязан указывать на writable persistent secret
volume; read-only Docker secret пригоден только для импортированного snapshot.

## Dashboard вместо отдельного login

Флаг `--dashboard` неоднозначен: основной `job-agent` не должен скрыто запускать
второй Compose-сервис. Канонический CLI контракт —
`--presenter terminal|dashboard|vnc|auto`:

- `terminal` показывает challenge и принимает ответ в текущем TTY;
- `dashboard` создаёт auth session в backend и выводит короткоживущий URL;
- `vnc` оставляет headed browser для сложного/изменившегося flow;
- `auto` выбирает terminal для поддерживаемой captcha/QR, затем dashboard и
  только потом VNC fallback.

Dashboard сам вызывает то же API, показывает этапы identifier/OTP/captcha,
состояние exchange и факт сохранения credentials, но никогда не возвращает
токены браузерному JavaScript после записи. В UI пользователь выбирает профиль
и credential sink из заранее разрешённых backend paths; произвольный путь с
клиента не принимается.

## Captcha и QR в терминале

Challenge presenter получает short-lived blob по ID и выбирает renderer:

```text
auto -> Kitty graphics -> Sixel -> Unicode preview/file fallback
```

Предусматривается флаг
`--image-protocol auto|kitty|sixel|unicode|file`. Kitty renderer отправляет PNG
chunked base64-командами graphics protocol. Sixel renderer ограничивает размер
и палитру перед кодированием. QR не масштабируется интерполяцией и сохраняет
quiet zone; captcha масштабируется без потери читаемости. Терминал без
поддерживаемой графики получает защищённый временный файл или dashboard URL.

Изображение, URL и введённый ответ живут только до deadline challenge, не
попадают в постоянную БД и удаляются после успеха, отмены или timeout. Это
ручное подтверждение, а не распознавание или обход captcha.

## Backend lifecycle

Минимальная state machine:

```text
created -> waiting_identifier -> waiting_otp/password/captcha
        -> exchanging -> storing -> completed
        `-> expired | cancelled | failed
```

Нужны узкие endpoints создания/чтения session, отправки текущего challenge,
отмены и повторного запроса кода. SSE сообщает только state transition и
redacted metadata. Credential writer и token refresh используют один
per-profile lock и revision CAS; после частичного сбоя повтор либо продолжает
ту же session, либо достоверно сообщает, был ли secret сохранён.

## Критерии готовности

1. Login одного профиля завершается одинаково из CLI и dashboard.
2. JSON и dotenv содержат access/refresh token и проходят round-trip loader.
3. `--credential-output - > file.env` выдаёт в stdout только валидный dotenv.
4. Прямой output атомарен, `0600`, защищён от symlink/случайной перезаписи.
5. Kitty и Sixel проверяются snapshot-тестами; unsupported terminal использует
   безопасный fallback.
6. Captcha/OTP не появляются в логах, БД, task payload и dashboard history.
7. Refresh переживает рестарт, сериализуется по профилю и атомарно заменяет оба
   токена.
8. Expired/revoked credentials переводят только свой профиль в
   `auth_required`; остальные профили продолжают работать.
9. CLI/dashboard сверяют API contract version перед продолжением auth flow.

## Auth service

Пакет `auth/` управляет сессией и ручными шагами:

- `Service.Start` создаёт durable session и просит driver первый шаг;
- `Service.Submit` принимает identifier, OTP, password или captcha, переводит
  сессию через `waiting_*` → `exchanging` → `storing` → `completed`;
- ошибка driver с нормализованной категорией завершает только эту сессию
  статусом `failed`;
- при сбое записи secret после успешного exchange сессия честно помечается
  failed, потому что повтор не может восстановить одноразовый ответ;
- `Service.ChallengePayload` отдаёт PNG/URL из `MemoryChallengeStore`; payload
  удаляется при смене шага, отмене и завершении и живёт только в памяти.

## Порядок реализации

1. ✅ Credential record, reader/writer и JSON/dotenv round-trip.
2. ✅ Persistent auth session, challenge store, service и HTTP endpoints; SSE и
   runtime registration ждут HH driver.
3. HH browser login и OAuth token exchange внутри adapter-а.
4. ⏳ Terminal presenter: рендеры Kitty, Sixel, Unicode и file готовы в
   `presenter/`; CLI, TTY separation и API-клиент остаются.
5. Dashboard auth wizard и SSE.
6. Refresh/revoke/logout, restart recovery и browser E2E.
