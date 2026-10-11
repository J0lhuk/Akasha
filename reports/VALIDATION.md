# Проверка Akasha 0.2.0

Дата: 2026-10-11. Windows amd64 / Go 1.26.4, ОС сообщает 6 физических ядер и 12 логических потоков, RAM 31.9 GiB. Linux проверен в Docker. Другие приложения работали параллельно; это измерения указанного компьютера, не сравнение языков/СУБД в лаборатории.

## Реализация и проверки

| Область | Проверенный результат | Артефакт |
|---|---|---|
| Исследование | 200 непустых страниц GitHub Search, 328 релевантных материалов / 31 хост, 36 README в сравнении; хэши проверены | [метод](../research/METHOD.md), [реестр](../research/CATALOG.md), [integrity](research-integrity.json) |
| Windows Go | 44 теста/подтеста, 30 верхнего уровня, 0 ошибок; native inference включён; go vet прошёл | [сводка](test-summary.json), [JSONL](go-test.jsonl) |
| Coverage | 46.9% statements всего модуля, включая CLI/UI; не полное покрытие | coverage.out |
| Linux | go test -race ./... и go vet ./...; отдельный настоящий native inference с сетью none | [race/vet](linux-race.log), [offline](linux-offline-vector.json) |
| Codex | Реальный CLI/MCP/hook stdin/stdout; add для старого проекта, бюджет, prompt templates, cache; skill установлен и прошёл validator | [команды](command-smoke.json), [сквозной сценарий](postgres-encrypted-smoke.json) |
| Локальная модель | 384 компоненты, RU/EN 6/6 в top-5 против lexical 0/6 на малом semantic fixture | [native](vector-evaluation.json), [Python/Go parity](embedding-parity.json) |
| Аккаунты | Password login, защищённый session file, повторный процесс, отказ перезаписи файла, отзыв; UI cookie login/logout | [CLI](account-smoke.json), [UI](ui-0.2-verification.json) |
| PostgreSQL | PG 17.11, Timescale 2.30.2, Alembic, application role без superuser, 10 пользователей | [сервер](postgres-smoke.json) |
| Skills | Создание одной командой, описание/evidence/provenance, локальная версия + шифрованное облако, два клиента / две версии, backup и ACL | [локально](skill-smoke.json), [PostgreSQL](postgres-skill-smoke.json) |
| UI | Вход/выход, аккаунты, модель/ресурсы; dark theme, 390px без горизонтального переполнения, 0 console warnings/errors | [ресурсы](ui-resources-dark.png), [аккаунты](ui-accounts-dark.png), [вход](ui-login-dark.png) |

Ранее проверенные wiki/граф/история/конфликты/vault/source/Git/checkpoint остаются в regression suite. [Историческая проверка P7](VALIDATION-P7.md). Глобальная конфигурация Codex и trust hooks не менялись. Установлен только собственный skill `akasha`; вызов `$akasha`, штатный `/skills`, произвольный `/akasha` не обещается как встроенный dispatcher.

Повтор установки обнаружил временный Windows access/sharing отказ при переименовании staging-каталога. Добавлен ограниченный повтор только этих ошибок (до 1.27 s), без изменения прав; rollback восстанавливает backup независимо от отмены основного запроса и сообщает путь при отказе. Windows-тест держит настоящий файл без FILE_SHARE_DELETE, затем освобождает его и проверяет сохранность байтов.

## Контекст и токены: фиксированные 1000 записей / 20 вопросов

Вопросы [fixture](../testdata/retrieval.json) не изменены. Budget 1024, tokenizer o200k_base. Столбцы `fts_*` в старом формате JSON у hybrid означают весь `Manager.Search`.

| Метрика | Lexical + graph | Hybrid E5 + graph |
|---|---:|---:|
| Recall@5 | 1.00 | 1.00 |
| Ключевой текст сохранён | 20/20 | 20/20 |
| Пакеты в бюджете | 20/20 | 20/20 |
| Повторные пакеты — cache hit | 100/100 | 100/100 |
| Сокращение против top-5 оригиналов своего поиска | 82.00% | 89.00% |
| Search первый проход p95 | 6.00 ms | 42.54 ms |
| Context cache miss p95 | 63.04 ms | 239.81 ms |
| Context cache hit p95 | 1.01 ms | 2.01 ms |
| Чужая группа / неизвестный идентификатор | 0 источников | 0 источников |
| Восстановлено событий checkpoint | 1000/1000 | 1000/1000 |

[Lexical](evaluation.json), [hybrid](hybrid-evaluation.json). Первое построение 1623 фрагментов: 66.48 s; float32 payload 2,492,928 bytes. Первый проход запросов выполняется после индексации и загрузки модели, это не cold model startup. Повторный импорт 100 Go-файлов: 0 новых версий; 201 неизменных записей (файлы + символы + папка).

Сокращение относится к длинным архивным документам этого набора. Top-5 у способов поиска отличается, знаменатель различается. Это **не измерение оплаченных токенов, не экономия всей истории Codex и не доказательство качества кода модели**. Большой coding benchmark, provider cache hit и LLM-компрессия не заявлены.

Сохранены неуспешные/промежуточные результаты: [точный идентификатор P7](evaluation-before-identifier-fix.json), [INT8 batching](vector-evaluation-before-batching-fix.json), [ложные dense-соседи для неизвестной строки](hybrid-before-identifier-guard.json), [пересчёт статистики при cache hit](hybrid-before-cache-key-optimization.json), [параллельная нагрузка хоста](hybrid-host-load-measurement.json). Последний дал p95 нового пакета 516 ms; он не скрыт. В финальном замере тяжёлые проверки разделены.

У первого hybrid-прогона проверка `foreign_group_absent` была false из-за семантически похожих **разрешённых** записей на точный canary-запрос, не чтения чужой БД. Политика одиночных code identifiers теперь лексическая. Импортный fixture внутри `.local` получил собственную Git-границу, чтобы parent ignore не скрывал все 100 файлов; критерий проверяет фактические counts.

## Многопоточность и сервер

Профиль: 4 workers импорта, 4 inference threads, bounded queues/buffers; Go soft heap limit и отдельный native reserve. [Ресурсы/формулы](../docs/PERFORMANCE.md). На 1000 готовых векторов scan ускорен с 18.45–21.66 ms до 0.92–1.07 ms, выделения ~3.60 MB →145 KB на запрос; inference в этот микробенчмарк не входит.

Последний серверный сценарий: **400 запросов, 265.8 req/s, p95 97.70 ms**, 10 пользователей; 200 шифрованных тестовых событий сохранены. Десять редакторов одного проекта получили последовательности 1..10; повтор события не создаёт дубль. 0 потерянных метрик, 0 ожидающих в момент проверки. Revoke/logout/disable, reader deny, Alembic повторно и restart прошли; два реальных клиента перенесли исходники/Git/vault/context и skills.

Первоначальная синхронная запись метрик заменена bounded batch; подготовка первого Timescale chunk перенесена в миграцию. [До batch](postgres-before-metrics-batching.json), [до подготовки chunk](postgres-before-chunk-prewarm.json). Метрики — best effort с явным dropped counter, события памяти подтверждаются отдельно. Инвентаризация учитывает и остановленные контейнеры: сторонние сервисы могут перезапускаться независимо. Исходные container identities сохранены; команды cleanup затрагивают только уникальный тестовый Compose project.

## Поставка и ограничения

Windows/Linux 0.2.0 binaries, source ZIP с Compose/миграциями, SHA256 и licenses. Модель установлена локально; в ZIP веса не включены. Потребитель скачивает закреплённые файлы через `model-install` либо переносит проверенную установку той же ОС. [RAG](../docs/LOCAL_RAG.md), [аккаунты](../docs/ACCOUNTS_SERVER.md), [skills](../docs/SKILL_REPOSITORY.md).

Внешний production hub не развёрнут: реальный тест выполнялся локально в Docker. Для нового компьютера нужны HTTPS hub, аккаунт и отдельно переданный ключ проекта. Hook trust остаётся штатным действием пользователя. Сохраняется история Akasha и заявленные исходники/ключи, а не скрытое состояние модели/недокументированная БД Codex. Пределы размера, отсутствие ротации проектного ключа/OIDC/личных подписей — [SECURITY](../docs/SECURITY.md). Подтверждённое сокращение контекста не заменяет пилот на рабочих данных.
