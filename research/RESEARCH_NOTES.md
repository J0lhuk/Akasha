# Наблюдения исследования (до плана реализации)

## Уточнения владельца

2026-10-11: общая память ограничена своей группой проектов; между группами только явно разрешённый перенос. Облачная часть — собственный сервер с Docker.

## Что подтверждают источники

| Семейство | Полезный механизм | Ограничение / вывод для оценки |
|---|---|---|
| [Engram](https://github.com/Gentleman-Programming/engram) | Go, SQLite FTS5, проектный scope, CLI/MCP/HTTP, перенос памяти | Сам выбор Go не доказывает превосходство по скорости; нужна наша нагрузка и замеры. |
| [Basic Memory](https://docs.basicmemory.com/concepts/projects-and-folders) | Markdown, отдельные проекты, observations, wikilinks | Читаемые файлы помогают инспектировать память; индекс и источник истины должны иметь ясный порядок обновления. |
| [Graphiti](https://help.getzep.com/graphiti/graphiti/overview) | Происхождение фактов, время действия и время записи, граф и поиск | Автоматическая инвалидизация предыдущего факта не подходит требованию сохранять обе версии до решения. |
| [Letta](https://docs.letta.com/guides/agents/memory-blocks) | Маленькие закреплённые блоки и архивная память | Закреплять всю историю нельзя; даже полезный постоянный блок имеет стоимость на каждом обращении. |
| [Hindsight](https://hindsight.vectorize.io/developer/retrieval) | Несколько способов поиска, бюджет контекста, отдельная консолидация | В документации допускается превышение бюджета ради целого факта; Akasha нужен жёсткий предел и явное сообщение об исключённых фактах. |
| [Aider](https://aider.chat/docs/repomap.html) | Карта репозитория, ранжирование зависимостей под бюджет | Сначала символы и связи, полный текст только по запросу. Не приравнивать графовую центральность к истинности факта. |
| [Serena](https://oraios.github.io/serena/01-about/025_features.html) | Навигация по символам и ссылкам через языковой сервер | LSP требует отдельных runtime; базовый импорт не должен запускать неизвестный код проекта. |
| [Context Mode](https://github.com/mksglu/context-mode) | Сохранение больших результатов инструментов с последующим поиском | Проценты из README — заявления авторов. Нельзя считать, что MCP контролирует все чужие tool outputs в Codex. |
| [OpenViking](https://github.com/volcengine/OpenViking) | Иерархия, обзор → краткое содержание → оригинал | Иерархические представления полезнее бесконечного набора глобальных заметок. |
| [Beads](https://github.com/gastownhall/beads) | Зависимости задач, готовая работа, передача состояния между агентами | Память и план работы связаны, но краткая память не заменяет исходники, Git и историю решений. |
| [Mem0](https://docs.mem0.ai/core-concepts/memory-types) | Управляемая запись фактов, entity linking, раздельные уровни памяти | Текущая документация различает OSS и hosted; нельзя переносить результаты hosted benchmark на локальное приложение. |
| [Cognee](https://docs.cognee.ai/core-concepts/architecture) | Источники → chunks → граф, provenance | Много движков и LLM на этапе индексации увеличивают сложность и стоимость первого запуска. |
| [GraphRAG](https://microsoft.github.io/graphrag/index/architecture) | Иерархические summaries, кэш индексации, локальные и глобальные запросы | Предварительная LLM-обработка всего репозитория противоречит экономному offline-first старту. |
| [LLMLingua-2](https://arxiv.org/abs/2403.12968) | Сжатие как отбор информативных токенов | Требует отдельной модели, может влиять на точность; не включать как бесконтрольный фильтр исходного кода. |
| [LongMemEval](https://arxiv.org/abs/2410.10813), [LoCoMo](https://arxiv.org/abs/2402.17753) | Обновления знаний, многосессионный поиск, временные вопросы, отказ при отсутствии сведений | Разделять качество retrieval, качество ответа, latency и стоимость. Наш технический fixture не является полноценным прогоном этих benchmark. |
| [Obsidian links](https://obsidian.md/help/links), [graph](https://obsidian.md/help/plugins/graph) | Wiki-ссылки, backlinks, локальный граф, иерархия файлов | Граф интерфейса и граф ранжирования связаны, но выполняют разные задачи. |
| [Automerge](https://automerge.org/docs/reference/concepts), [Loro](https://loro.dev/docs/tutorial/version), [Yjs](https://docs.yjs.dev/api/faq) | Причинная история, инкрементальные обновления, работа offline | CRDT-сходимость не выбирает истинный факт. ACL нельзя надёжно накладывать на часть одного общего YDoc после получения данных клиентом. |
| [Syncthing](https://docs.syncthing.net/users/syncing.html), [Joplin](https://joplinapp.org/help/apps/conflict) | Сохранение конфликтных файлов, разделение синхронизации и резервирования | Нельзя копировать открытую SQLite с WAL как обычный файл. Нельзя обещать восстановление исключённых из sync файлов. |
| [Restic](https://restic.readthedocs.io/en/stable/100_references.html), [age](https://github.com/FiloSottile/age) | Контентные хэши, шифрованные версии, проверка целостности | Нужны пределы размера, проверка архивных путей и отдельная проверка восстановления. |
| [Casbin domains](https://casbin.org/docs/rbac-with-domains), [OpenFGA](https://openfga.dev/docs/modeling/roles-and-permissions) | Пользователь + область + ресурс + действие | Авторизация до поиска/выгрузки; разделение visibility, write и admin. Отзыв прав не удаляет уже скачанные копии. |
| [OWASP secrets](https://cheatsheetseries.owasp.org/cheatsheets/Secrets_Management_Cheat_Sheet.html) | Секреты отдельно от logs, поиска и обычных документов | Пароль в wiki не становится безопасным из-за приватного проекта. Шифрованные backup и отдельная процедура восстановления ключа. |
| [SQLite WAL](https://www.sqlite.org/wal.html), [backup](https://www.sqlite.org/backup.html), [FTS5](https://www.sqlite.org/fts5.html) | Транзакции, локальный поиск, согласованные backup | Один писатель; WAL не предназначен для синхронизации файлов через облачную папку. |

## Интеграция с Codex: установленные границы

Локально проверен `codex-cli 0.162.0-alpha.17.2` и наличие команд MCP, plugin, app-server.

- [Hooks](https://learn.chatgpt.com/docs/hooks): SessionStart и UserPromptSubmit могут добавлять additionalContext; PreCompact/Stop позволяют сохранять состояние. Для доверия hook требуется штатная проверка его определения в Codex. Формат transcript_path не является стабильным API.
- [MCP](https://learn.chatgpt.com/docs/extend/mcp): локальный stdio и удалённый Streamable HTTP; конфигурация может быть локальной для проекта.
- [Plugin packaging](https://developers.openai.com/plugins/build/plugins): пакет skills + MCP + hooks; включение плагина не означает доверие hook.
- [App-server](https://learn.chatgpt.com/docs/app-server): интеграция на уровне протокола допустима, но перехват недокументированной БД приложения не нужен для памяти проекта.
- [Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching): стабильные префиксы помогают кэшу провайдера; точные cache hits видны по фактической telemetry. Локальный cache hit не равен скидке за токены модели.

## Критерии отбора источников

Репозитории с общими словами `memory`, `token`, `map` могут относиться к аппаратной памяти, криптовалютам, картографии или computer vision. Такие результаты не становятся рекомендациями автоматически. Обзор axion physics `arXiv:2404.09036` исключён как нерелевантный. Индексы документации, llms.txt и страницы участия в разработке не входят в итоговое число технических материалов.

Объём исследования выполнен: [реестр 328 материалов](CATALOG.md), [200 страниц выдачи GitHub](GITHUB_SEARCH.md), [сводные счётчики](summary.json). Решения и последовательность реализации зафиксированы в [плане](../docs/IMPLEMENTATION_PLAN.md).
