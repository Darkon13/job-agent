# `employer_groups`

Именованные группы работодателей для `applications.employer_rules`. Группа
сопоставляется по точному platform employer ID, точному нормализованному alias
или вложенной группе; циклы запрещены.

## Пример

```json
"employer_groups": [
  {
    "tag": "marketplaces",
    "include": ["known-good-shops"],
    "rules": [{"platform": "hh", "employer_id": "1740", "name": "Ozon"}]
  },
  {
    "tag": "known-good-shops",
    "rules": [{"name": "Wildberries"}, {"name": "Яндекс"}]
  }
]
```

| Поле | Тип | Обяз. | Описание |
|---|---|---|---|
| `tag` | string | **да** | Имя группы; на него ссылаются `applications.employer_rules[].employer_groups` и `include` других групп. |
| `rules` | array | нет | Точные правила сопоставления работодателя. |
| `include` | array | нет | Другие группы, включаемые в эту (вложенность). |

## `rules[]`

| Поле | Тип | Обяз. | Описание |
|---|---|---|---|
| `platform` | string | нет | Платформа правила, например `hh`; пустая — любая. |
| `employer_id` | string | нет | Точный platform employer ID. |
| `name` | string | нет | Точный нормализованный alias работодателя. |

У правила должен быть заполнен хотя бы один идентификатор (`employer_id` или
`name`); сравнение имён — точное, без fuzzy-merge. Match evidence (тип
совпадения, при вложенности — через какую группу) сохраняется в причине
решения отклика.

## Связанные документы

- [Отклики](applications.md) — `employer_rules` и действия `skip`/`review`;
- [Desired state](../../profile-desired-state-and-routing.md) — общий registry
  named actions и routing.
