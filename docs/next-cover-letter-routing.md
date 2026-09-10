# Следующая задача: пулы сопроводительных и группы работодателей

Статус: `in progress`. Файловые message pools, воспроизводимый выбор, employer
groups, policy routing, первый model provider с fallback, durable model
provenance и evidence-based groundedness реализованы. Из этого среза остаются
отдельный workflow ручной проверки модельного draft без повторной генерации и
его end-to-end approval smoke.

Явные resume facts уже загружаются из ограниченного файла, связываются с
конкретным resume ID и входят в единый model/template context. Детерминированный
output safety validator отсекает служебную обёртку, placeholders и не
подтверждённые контекстом числа и контакты.

Общий контракт `rule_sets → policy rules → named action`, а также processors,
которые разделяют детерминированные преобразования и LLM, зафиксирован в
[`docs/profile-desired-state-and-routing.md`](profile-desired-state-and-routing.md).
Этот документ остаётся владельцем конкретного application/message среза.

## Зачем

Сейчас профиль выбирает один статический текст либо один шаблон. Следующий срез
должен сделать сопроводительное результатом маршрутизации: готовый текст из
именованного пула, особая политика для группы работодателей либо генерация
моделью с проверяемым fallback.

Группы работодателей нужны как переиспользуемые rule-set объекты, похожие на
списки маршрутизации sing-box. Вместо повторения ID и вариантов названий Ozon,
VK, Wildberries и их дочерних компаний search/application rules ссылаются на
один или несколько тегов групп.

## Целевые объекты

### Именованный пул сопроводительных

Имя файла является коротким именем пула: например,
`messages/backend.json` доступен как `backend`. Файл содержит один или несколько
готовых текстов/шаблонов и явную стратегию выбора. Выбранный текст, имя пула и
варианта попадают в сохранённый результат подготовки `Application`, поэтому
retry не выбирает другое письмо после изменения конфигурации. Отдельный digest
исходного файла в application provenance ещё предстоит добавить.

Минимальные стратегии:

- `first` — первый подходящий вариант;
- `stable_hash` — воспроизводимый выбор по profile/vacancy без случайной смены
  при повторе задачи.

Сейчас реализованы `first` и `stable_hash`. `round_robin` отложен до появления
отдельного persistent состояния selector: эмулировать его process-local
счётчиком нельзя, иначе рестарт изменит последовательность.

### Группа работодателей

`employer_group` содержит `tag` и явные признаки принадлежности:

- platform-specific employer ID — наиболее сильное совпадение;
- точные нормализованные названия и aliases;
- при необходимости домены и явно перечисленные дочерние группы;
- ссылки на другие группы с проверкой циклов.

Принадлежность не должна выводиться неявно по одному похожему слову. Результат
matcher содержит совпавшие группы и доказательство: ID, alias, domain или
вложенная группа.

Application rule может ссылаться на одну или несколько групп и задавать:

- `include` или `exclude`;
- отдельный search/application route;
- именованный пул сопроводительных;
- специальную operator chain;
- ручное подтверждение либо пропуск.

### Генератор моделью

Model operator получает структурированный и ограниченный контекст:

- выбранные факты профиля и резюме;
- полную вакансию и ключевые навыки;
- работодателя и совпавшие employer groups;
- пользовательскую инструкцию и ограничения платформы.

Базовая схема контекста уже существует в application operator: `ApplicationID`,
`ProfileID` и вложенный `Vacancy` с platform/external ID, URL, названием,
работодателем, состоянием, датой публикации, description, key skills и
нормализованными attributes полной карточки. `Resume` содержит platform resume
ID, tag/digest файла и только явно объявленные пользователем `facts`. Шаблоны и
model adapter используют один объект, не читают platform payload и не собирают
контекст повторно.

Конфигурация ссылается на provider/model по тегу; конкретная компактная модель
не зашивается в core. Результат проходит formatter и validator. Нельзя
добавлять опыт, достижения или навыки, которых нет во входном контексте.
Timeout, rate limit, пустой или невалидный ответ переходят к указанному
именованному пулу, а не блокируют всю campaign.

Сохраняются итоговый текст и отдельная versioned provenance: источник, tag
оператора, модель, версия prompt/template, provider response ID,
input/output/evidence digests, число evidence claims, resume facts tag/digest и
безопасная категория fallback. Полные prompt,
facts, provider error и внутреннее reasoning модели не сохраняются и не
отправляются работодателю.

Model response содержит `text` и claims со списком `{path, quote}`. Локальный
validator, независимо от provider schema, требует точные уникальные claims,
полное покрытие буквенно-цифрового текста, разрешённые JSON Pointer только в
`resume.facts`/`vacancy`, scalar leaf и буквальное присутствие quote в source и
claim. Контакты и числа сверяются с цитатами своего claim. При любой ошибке
результат классифицируется как `invalid_output` и не покидает operator chain.
Такой контракт делает заявленное evidence воспроизводимо проверяемым, но не
является общим доказательством семантической истинности естественного языка.

## Целевая цепочка

```text
full vacancy + employer
-> employer-group matcher
-> include/exclude/routing rules
-> company-specific operator or message pool
-> model operator when configured
-> named-pool fallback
-> formatter
-> groundedness/length validator
-> approval policy
-> persist prepared result
-> submit
```

## Порядок реализации

1. **Выполнено:** файловые именованные message pools загружаются с проверкой
   схемы; top-level `employer_groups` доступны из profile rules.
2. **Выполнено:** matcher предпочитает точный platform ID, поддерживает точные
   нормализованные aliases и вложенные группы, отклоняет циклы и возвращает
   доказательство совпадения. Fuzzy/substring matching отсутствует намеренно.
3. **Выполнено для profile-level selector:** одиночный
   `message_template_file` теперь принимает pool, старый `{ "template": ... }`
   остаётся совместимым.
4. **Выполнено:** `first`/`stable_hash` выбирают вариант воспроизводимо, а
   подготовленный текст сохраняется до submit/retry. Provenance содержит digest
   нормализованного содержимого всего пула и выбранный template tag.
5. **Выполнено:** упорядоченные profile-level rules выбирают отдельный pool,
   `skip` либо `review`; первое совпавшее правило побеждает, а причина решения
   содержит группу и evidence.
6. **Выполнено:** общий model-operator port, OpenAI Responses adapter,
   timeout/error classification и fallback на готовый пул. API key приходит из
   environment, запрос использует `store:false`, а модель выбирается конфигом.
7. **Выполнено для автоматической цепочки:** длина, управляющие символы,
   JSON/code fence, placeholders, новые числа, URL и e-mail приводят к
   безопасному fallback.
   Явные resume facts входят в контекст; structured evidence contract локально
   сверяет claims, JSON Pointer и точные quotes. Он не доказывает семантику
   естественного языка, поэтому будущие сомнения должны отправляться в approval.
8. **Частично выполнено:** model success, timeout, rate-limit, invalid output,
   parent cancellation, provider HTTP contract, config, routing, provenance
   v1/v2/v3 round-trip и миграция старых rows покрыты. Нужны draft-review
   workflow и один `approval` smoke.

## Критерий готовности

В одном конфиге можно определить несколько групп работодателей и несколько
файлов-пулов, назначить особый пул или model operator группе и использовать
общий fallback для остальных вакансий. Один и тот же подготовленный Application
получает неизменный текст при любом retry, а решение объясняет совпавшую группу,
выбранный пул/operator и причину fallback.
