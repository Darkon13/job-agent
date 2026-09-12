## Что изменено

<!-- Кратко: какую проблему решает PR и что именно поменялось. -->

## Как проверялось

<!-- Команды и сценарии: make verify, make smoke, ручные проверки. -->

- [ ] `make verify` зелёный
- [ ] для runtime-изменений `make smoke` зелёный
- [ ] для browser-worker `npm run typecheck` и `npm test`

## Что не проверено

<!-- Например: живой вызов платформы, требует аккаунта или credentials. -->

## Чек-лист

- [ ] platform-specific код только в `adapters/<platform>`
- [ ] ошибки нормализованы в `core.OperationError`, без секретов в сообщениях
- [ ] тесты на success и failure-ветки
- [ ] обновлены `docs/changelog.md` и контрактная документация в `docs/`
- [ ] секреты, `data/` и персональные данные не попали в diff
