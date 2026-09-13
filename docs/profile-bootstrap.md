# Декларативное заполнение профиля и резюме

`ProfileBootstrap` описывает желаемые поля аккаунта и резюме одним JSON-файлом.
Команда сначала читает актуальную схему HH, строит immutable redacted plan и по
умолчанию ничего не изменяет:

```bash
go run ./cmd/job-agent profile bootstrap \
  --api http://127.0.0.1:8081 \
  ./data/primary-resume.json
```

Для ожидаемого первого заполнения есть короткая команда, которая сразу создаёт
plan и ставит его в durable queue:

```bash
job-agent startup --api http://127.0.0.1:8081 ./data/primary-resume.json
```

Это удобный alias режима `profile bootstrap --apply`; apply всё равно не пишет
в HH внутри CLI-запроса, а ставит идемпотентную background-задачу.

## Автоматический bootstrap при запуске сервиса

Manifest можно связать с runtime-профилем в основном config:

```json
{
  "tag": "primary",
  "adapter": "hh-main",
  "state_file": "./data/profiles/primary.json",
  "bootstrap": {
    "source": "../../data/primary-resume.json",
    "when": "empty"
  },
  "enabled": true
}
```

Относительный `source` считается от каталога config, а не от текущего рабочего
каталога процесса. Loader заранее декодирует versioned manifest и требует,
чтобы `spec.profile_id` совпадал с `profile.tag`.

После привязки авторизованного reader/writer startup выполняет один bounded
read только объявленных путей. Для `when: empty` условие выполняется, когда
каждый путь отсутствует либо содержит `null`, пустую строку, пустой массив или
пустой объект. Числа и boolean, включая `0` и `false`, считаются заполненными.
Если заполнен хотя бы один объявленный путь, весь bootstrap пропускается: это
защищает частично заполненный профиль от неявной перезаписи.

При выполненном условии используется обычный trusted read → immutable plan →
durable `profile_state.apply`. Поэтому desired-значения не копируются в task,
повторный запуск дедуплицируется, а task уже находится в БД до запуска workers
и служит барьером для старых и новых `application.submit` того же профиля.
Профиль в `auth_required` не блокирует запуск всего сервиса: bootstrap ожидает
авторизации и будет проверен при следующем запуске.

Сейчас runtime намеренно принимает только `when: empty`. `missing_resume` и
`publish: true` отклоняются при загрузке config, пока adapter не умеет отдельно
создавать и публиковать резюме. Эти значения не игнорируются молча.

Порт `8081` — dashboard/reverse proxy из Compose. При локальном запуске backend
напрямую используется `http://127.0.0.1:8080`. Чтобы поставить созданный plan в
durable queue, нужен явный флаг:

```bash
go run ./cmd/job-agent profile bootstrap \
  --api http://127.0.0.1:8081 \
  --apply \
  ./data/primary-resume.json
```

Сервис `job-agent` должен продолжать работать: `--apply` возвращает ID задачи,
а изменение выполняет background worker. Повторный запуск с тем же manifest и
тем же фактическим состоянием возвращает `no_changes`. Если резюме изменилось
между plan и apply, compare-before-write остановит задачу конфликтом вместо
перезаписи новых данных.

Apply-задача имеет системный максимальный priority. Пока она активна либо её
последний terminal result равен `failed`, `application.submit` этого профиля
остаётся в очереди без увеличения attempts. Операции других профилей не
блокируются, а уже начавшуюся отправку новый apply не прерывает.

После исправления причины failure тот же proposal запускается новым bounded
retry-cycle:

```text
POST /api/v1/profile-state/proposals/{proposal_id}/retry
```

Если изменение сознательно больше не требуется, барьер снимается отдельным
аудитируемым решением:

```text
POST /api/v1/profile-state/proposals/{proposal_id}/dismiss
```

Обе команды принимают только пустое тело и только задачу в `failed`. `retry`
переводит её в `new`, сбрасывает attempts и сохраняет исходный idempotency key;
`dismiss` переводит её в terminal `dismissed`, сохраняя последнюю ошибку.

## Формат

Полный пример находится в
[`deploy/profile-bootstrap.example.json`](https://github.com/Darkon13/job-agent/blob/main/deploy/profile-bootstrap.example.json).
Control envelope имеет фиксированные `api_version: job-agent/v1`,
`kind: ProfileBootstrap`, уникальное `metadata.name` и `spec.profile_id` из
runtime config. `ownership` пока допускает только `declared_fields`: Job Agent
владеет ровно листьями, которые присутствуют в `state`, и не очищает остальные
разделы.

Для профиля с сохранённой browser-сессией основной формат записывается так:

```text
resumes.<hh-resume-id>.web.<field>
resumes.<hh-resume-id>.web_profile.<field>
```

Это реальный JSON-контракт текущего web-редактора HH. Обёрнутые поля всегда
задаются массивами, даже если значение одно. Личные поля `firstName`,
`lastName`, `middleName`, `birthday`, `gender`, `area`, гражданство и желаемые
районы находятся в `web_profile`. Поля самого резюме — `title`,
`professionalRole`, `skills` («О себе»), `keySkills`, языки, форматы и виды
занятости — находятся в `web`. Например, `title: ["Backend-разработчик"]`,
`area: [1]`, а `skills` содержит ноль или один текст. Для удаления используется
пустой массив.

Составные поля передаются массивами объектов в форме HH: `experience`,
`primaryEducation`, `additionalEducation`, `certificate`, `recommendation` и
остальные виды образования. Для опыта базовые имена — `companyName`,
`position`, `startDate`, `endDate`, `description`; при редактировании уже
существующей записи можно сохранить выданные HH идентификаторы и дополнительные
поля. Объявленный массив атомарно заменяет соответствующий раздел. Необъявленные
поля вообще не входят в POST и остаются без изменений.

Поддерживаемый allowlist не включает read-only telemetry и статусы, которые HH
возвращает рядом с резюме. Неизвестное имя отвергается до сетевого запроса.
`keySkills` — массив точных строковых тегов: перевод, fuzzy merge и
автоматическое исправление регистра не выполняются.

Перед реальным apply следует сверить идентификаторы и форму значений с текущим
HH wizard/dictionaries. Backend возвращает только операции и JSON Pointer пути,
но не контактные данные, старые тексты или новые значения. Сам файл содержит
персональные данные: держите его вне Git (например, в `data/`) и ограничьте
права чтения.

## Транспорты

- Browser transport читает cookie-authenticated JSON через
  `GET /applicant/resume?resume=<id>` и
  `GET /shards/applicant/profile/get_full_data`; отправляет только объявленные
  поля через `POST /applicant/resume/edit` и
  `POST /shards/applicant/profile/update`. После POST выполняется повторный GET.
- OAuth/API transport дополнительно работает с native `resume_profile`
  секциями `profile`, `resume`, `creds`, `additional_properties` через
  `GET/PUT /resume_profile/{resume_id}`. Перед PUT он подмешивает объявленные
  поля в свежий документ и после PUT выполняет read-back.
- Старый путь `resumes.<id>.about` сохранён для совместимости, но в новом
  manifest следует использовать `web.skills`; одновременно объявлять оба нельзя.
- Создание отсутствующего резюме и последующий publish остаются отдельными
  внешними действиями и не выполняются неявно этой командой.

Manifest отправляется только доверенному локальному или tunnel-bound dashboard
endpoint. В SQLite immutable proposal содержит snapshot, необходимый для
compare-before-write; поэтому каталог данных сервиса должен храниться на
защищённом volume.
