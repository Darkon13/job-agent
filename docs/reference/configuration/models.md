# `models`

Провайдеры моделей. На `tag` провайдера ссылаются `applications.model`,
`employer_rules[].model`, `tailoring.*.model` и `answers.model`. Ключ
читается только из переменной окружения.

## Пример

```json
"models": [
  {
    "tag": "deepseek",
    "type": "openai_chat",
    "model": "deepseek-flash",
    "base_url": "https://api.deepseek.com/v1",
    "api_key_env": "DEEPSEEK_API_KEY",
    "max_output_tokens": 2000,
    "reasoning_effort": "none"
  }
]
```

| Поле | Тип | Обяз. | По умолчанию | Описание |
|---|---|---|---|---|
| `tag` | string | **да** | — | Имя провайдера для ссылок из policy. |
| `type` | string | **да** | — | `openai_responses` — Responses API (`POST /responses`); `openai_chat` — OpenAI-совместимый chat completions (DeepSeek, шлюзы, локальный vLLM) через JSON mode. |
| `model` | string | **да** | — | Имя модели у провайдера. |
| `base_url` | string | нет | `https://api.openai.com/v1` для `openai_responses`, `https://api.deepseek.com/v1` для `openai_chat` | Свой endpoint. Только HTTPS, либо HTTP на loopback. |
| `api_key_env` | string | нет | `OPENAI_API_KEY` | Имя переменной окружения с ключом. Значение не попадает в конфиг и логи. |
| `max_output_tokens` | number | нет | `1024` | Предел ответа модели, 1..32768. Для reasoning-моделей учитывает и «размышления» — ставьте с запасом. |
| `reasoning_effort` | string | нет | — | Для reasoning-моделей: `none`, `low`, `medium`, `high`. `none` отключает CoT и экономит токены (`openai_chat`). |

## Контракт structured output

- `openai_responses` получает строгую JSON-схему в `text.format` и `store:false`;
- `openai_chat` работает через `response_format: json_object`, а требуемая
  форма ответа дописывается в системный промпт; ответ разбирается локальным
  decoder-ом, поэтому выдуманные имена полей не проходят;
- провайдер нейтрален к задаче: письмо, ответ на вопрос, выбор навыков,
  переписывание «О себе» используют один контракт.

## Ошибки и fallback

- timeout, rate limit, 5xx и временные сетевые ошибки классифицируются в
  `ModelFailure*` и не блокируют campaign: operator выбирает fallback;
- для tailoring временные ошибки дают безопасный «оставить как есть»;
- успешный результат сохраняется до внешнего действия, поэтому retry не
  вызывает модель повторно.

Готовые рецепты — в [шпаргалках](../recipes.md#модель-для-сопроводительных).
