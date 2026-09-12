# Временный tailoring резюме перед откликом

## Назначение

Tailoring — опциональный per-application workflow. Он временно подстраивает
выбранное резюме под полную вакансию, отправляет отклик и возвращает резюме в
точное исходное состояние. Это не новое постоянное desired state профиля и не
скрытая часть генератора сопроводительного письма.

Первый полезный набор полей:

- ключевые навыки/теги;
- «О себе»;
- описания мест работы.

Контракт не ограничивается этими полями: policy может разрешить любой путь,
который умеет читать и записывать platform adapter. Неизвестный или
непрочитанный путь нельзя временно менять, потому что его невозможно надёжно
восстановить.

## Место в application pipeline

```text
candidate ready
-> reserve daily budget
-> acquire submit pacing slot
-> read full vacancy and current resume
-> build immutable tailoring plan
-> apply and read-back tailored state
-> verify the same tailored revision immediately before submit
-> submit or reconcile ambiguous result
-> restore exact pre-tailoring snapshot
-> read-back restored state
-> finish application workflow
```

Квота и будущий pacing slot проверяются до изменения резюме. Поэтому резюме не
остаётся временно изменённым, пока задача ждёт следующего дня или своей
15–25-секундной очереди. Один профиль выполняет только одну такую saga за раз;
другие профили независимы.

Tailoring является отдельным durable workflow с фазами `planned`, `applying`,
`applied`, `submitting`, `restoring`, `restored` и `recovery_required`.
Физически фазы могут исполняться несколькими узкими задачами, но одна запись
saga остаётся source of truth и владеет mutation lease профиля до restore.

## Вход processor

Processor получает структурированный и ограниченный контекст:

```json
{
  "application": {"profile_id": "primary", "resume_id": "..."},
  "vacancy": {
    "title": "Go developer",
    "description": "...",
    "key_skills": ["Go", "PostgreSQL"]
  },
  "employer": {"name": "...", "groups": ["marketplaces"]},
  "resume": {"state": {}, "allowed_paths": []},
  "policy": {"skill_strategy": "add_vacancy_skills"}
}
```

Полный контекст вакансии берётся из сохранённого full read, контекст компании
— из нормализованного работодателя и rule sets, текущее резюме — из trusted
adapter read. Секреты, cookies и access tokens в processor не передаются.

Processor возвращает не готовый platform request, а типизированный patch с
provenance и объяснимыми решениями:

```json
{
  "changes": [
    {"path": "/resumes/.../resume/skill_set", "value": ["Go", "PostgreSQL"]},
    {"path": "/resumes/.../about", "value": "..."}
  ],
  "skills": {
    "add": [{"value": "PostgreSQL", "evidence": "vacancy.key_skills"}],
    "remove": []
  }
}
```

Поддерживаются deterministic script/processor и model processor, например
небольшая модель OpenAI. LLM не применяет изменения сама: output проходит
schema validation, allowlist путей, platform limits и semantic diff.

## Два режима обновления резюме

1. **Подстройка под вакансию перед откликом** (`resume.tailor` saga). Временная
   адаптация: добавляются теги и навыки из вакансии, нужные блоки выдвигаются в
   «О себе», в описании опыта подчёркиваются релевантные темы. Snapshot
   снимается до изменения, отклик отправляется на tailored-состоянии, после
   submit/отказа выполняется restore. Внешний drift переводит saga в
   `recovery_required`.
2. **Ежедневное полное обновление** (`resume.rewrite`, аналог «мы ещё в
   поиске»). Периодический рерайт: перефразирование достижений, смена порядка
   блоков, актуализация формулировок. Выполняется отдельным job без привязки к
   конкретной вакансии; исходная версия восстанавливается из snapshot только
   по явной команде, потому что это осознанно новое состояние резюме.

Оба режима используют один processor-контракт: структурированный patch по
объявленным путям, semantic diff, validator и optimistic concurrency. Модель
получает только обезличенный контекст резюме и вакансии; готовый patch
описывает пути и новые значения, но не содержит контактов.

## Политика навыков

Базовая стратегия `add_vacancy_skills`:

1. нормализовать все `vacancy.key_skills`;
2. объединить их с текущими навыками без дублей с учётом регистра;
3. не удалять существующие навыки без явного решения processor;
4. применить platform limit и сохранить объяснение отброшенных значений;
5. сохранять отдельно `added`, `removed`, `kept` и evidence.

Таким образом, по умолчанию добавляются все допустимые теги вакансии. Удаление
— отдельное решение, а не побочный эффект ограничения массива. Если места не
хватает, processor ранжирует кандидатов, а validator не допускает молчаливой
потери исходных навыков.

## Restore и сбои

До apply сохраняются encrypted/private snapshot разрешённых полей, remote
revision и digest. Restore строится именно из snapshot, а не из постоянного
config resource, который мог измениться во время отклика.

- успешный, уже существующий и гарантированно неуспешный отклик завершаются
  restore;
- неоднозначный submit сначала проходит reconciliation, затем restore;
- retry после гарантированного неуспеха начинает новую saga с новым snapshot;
- restore применим только если текущее состояние совпадает с tailored digest;
- внешний drift не перезаписывается: saga становится `recovery_required`,
  блокирует следующий tailoring этого профиля и показывает безопасный diff;
- restart возобновляет незавершённую фазу идемпотентно.

Audit хранит application ID, processor tag/version, digests и изменённые paths,
но не тексты резюме. Snapshot удаляется после подтверждённого restore согласно
retention policy.

## Порядок реализации

1. ✅ Ввести durable `ApplicationTailoring` и per-profile mutation lease.
2. ✅ Добавить config policy, typed processor input/output и deterministic
   `add_vacancy_skills`.
3. ✅ Переиспользовать `ProfileStatePlanner` для immutable apply/restore proposals.
4. ✅ Связать application submit с фазами saga и обязательной компенсацией.
5. ✅ Подключить model processor с fallback на deterministic policy.
6. ✅ Показать plan, skill diff, restore/recovery status в dashboard.
7. ✅ Добавить model about processor: обезличенный контекст, локальная
   валидация и chain с навыками.
8. ⬜ Связать `resume.rewrite` (ежедневный рерайт) с тем же processor-контрактом.

## Model processor

Если в `tailoring.skills.model` задан провайдер, processor просит модель выбрать
подмножество `key_skills` вакансии, которое помещается в лимит. Модель не
редактирует резюме: её решения проверяются по исходным навыкам и вакансии,
неизвестные значения и превышение лимита отклоняются, после чего используется
детерминированный `add_vacancy_skills`. Провайдер повторно использует
зарегистрированный `models[]` и structured output; при timeout, rate limit,
provider failure или невалидном ответе срабатывает deterministic fallback.

## Model about rewrite

Блок `tailoring.about` переписывает «О себе» под конкретную вакансию. Модель
получает текущий текст, обезличенные `resume_facts` (личные значения из
`facts.placeholders` уже заменены плейсхолдерами, объявление удалено), название
вакансии и её `key_skills`. Модель не является источником фактов: локальный
validator отклоняет пустой и превышающий `maximum_runes` текст, служебные
JSON/code-fence ответы, управляющие символы, незаявленные плейсхолдеры и
неподтверждённые числа, URL или e-mail. После валидации плейсхолдеры локально
заменяются реальными значениями, и только затем строится override.

Любая ошибка модели или невалидный ответ означает «оставить текущий текст»:
about processor возвращает пустой план без override, а остальные processor-ы
продолжают работу. Поэтому сбой about не блокирует ни навыки, ни отклик.

Разрешённый путь выводится из `resume` профиля:
`/resumes/{resume}/web/skills`. Policies `skills` и `about` можно включать
независимо; если включены обе, processors объединяются в chain, а совпадающие
пути отклоняются на этапе plan.

## Dashboard

Активная saga видна в таблице откликов отдельной колонкой: фаза (`planned`,
`applying`, `applied`, `submitting`, `restoring`), добавленные и удалённые
навыки и причина `recovery_required`. API отдаёт только redacted diff:
строковые массивы показывают добавленные/удалённые значения, остальные поля —
только факт изменения без содержимого. Завершённые (`restored`) sagas в списке
не показываются, потому что временное состояние уже снято.

## Конфигурация

Tailoring включается явно в профиле и работает только в режиме `submit`:

```json
{
  "tag": "primary",
  "resume": "1234567890abcdef",
  "applications": {
    "mode": "submit",
    "tailoring": {
      "skills": {
        "enabled": true,
        "maximum": 30,
        "model": {
          "provider": "openai-main",
          "prompt_version": "v1",
          "instruction": "Prefer the skills most relevant to the vacancy.",
          "timeout": "30s"
        }
      },
      "about": {
        "enabled": true,
        "maximum_runes": 600,
        "model": {
          "provider": "openai-main",
          "prompt_version": "v1",
          "instruction": "Highlight the Go and PostgreSQL experience relevant to this vacancy.",
          "timeout": "30s"
        }
      }
    }
  }
}
```

`maximum` — верхняя граница навыков в резюме; processor добавляет допустимые
`key_skills` вакансии, не удаляя существующие, и отклоняет план, если лимит
превышен. Разрешённый путь выводится из `resume` профиля
(`/resumes/{resume}/web/keySkills`), поэтому пользователь не задаёт JSON Pointer
вручную. Блок `model` у навыков необязателен и ссылается на зарегистрированный
`models[]`; без него работает только детерминированная политика.

`about` требует `model` (переиспользуется тот же `models[]`) и
`resume_facts_file`, потому что переписывать «О себе» без проверяемых фактов
нельзя. `maximum_runes` ограничивает длину результата; ориентируйтесь на лимит
платформы. Без блока `tailoring` handler работает как раньше.

