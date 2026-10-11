# Репозиторий skills Akasha

## Создать skill по итогам работы одной командой

В чате подключённого проекта:

```text
$akasha skill-create payment-retry по итогам этой работы, сохрани локально и в репозитории; синхронизируй с подключённым облаком
```

Агент выделяет из завершённой работы или исследования повторяемую процедуру, условия применения, проверки и действия при ошибках. Если результат ещё не сохранён в памяти, сначала записывает краткое подтверждённое решение с источниками. Затем одна операция `skill-create` создаёт пакет. CLI принимает готовую процедуру; он сам не вызывает LLM и не читает скрытые чаты Codex.

```powershell
akasha skill-create --project atlas --name payment-retry --description 'Use when implementing idempotent payment retries.' --file .\procedure.md --query 'Проверенный порядок повторов оплаты' --url https://memory.example.com --hub-token-file D:\Protected\akasha-session.credential
```

Без `--url` сохраняются локальная папка и версия репозитория группы. С `--url` та же команда дополнительно выполняет шифрованную отправку. При ошибке сети локальная версия остаётся, результат сообщает об ошибке; повтор отправки — `skill-sync`. Для первого подключения нужны логин/защищённая сессия hub и открытый локальный vault.

Пакет содержит `SKILL.md`, `references/evidence.md`, `references/provenance.json` и `agents/openai.yaml`. Provenance фиксирует проект, группу и ID исходных версий. По умолчанию папка находится в `<data>/spaces/<group>/generated-skills/<name>/<sha256>`. Повтор неизменного содержимого идемпотентен, изменённое сохраняется отдельной версией. Для сохранения границ группы производный skill не включается автоматически: `allow_implicit_invocation: false`. Установка в глобальные skills — отдельная явная команда `skill-install`.

Проверены реальная команда, два компьютера, разные версии одного имени, сохранение прежней установки и запрет чужой группы: [CLI smoke](../reports/skill-smoke.json), [PostgreSQL cloud smoke](../reports/postgres-skill-smoke.json). Сгенерированный пакет прошёл `quick_validate.py` установленного skill-creator.

## Публикация и установка существующих skills

Репозиторий общий внутри выбранной группы. Каждый пакет — неизменяемая версия с именем, описанием из `SKILL.md`, автором, меткой версии и SHA-256 всех файлов. Два разных пакета с одним именем/меткой сохраняются одновременно; при установке выбирается точный `id` из каталога. Повторная публикация того же содержимого не создаёт копию.

```powershell
akasha skill-installed
akasha skill-publish --space work --file 'C:\Users\me\.codex\skills\review' --label '1.0'
akasha skill-list --space work
akasha skill-versions --space work --name review
akasha skill-download --space work --id VERSION_ID --out 'D:\review-copy'
akasha skill-install --space work --id VERSION_ID
akasha skill-sync --space work --url https://your-hub.example
```

Из чата: `$akasha skills publish <папка>`, `$akasha skills list`, `$akasha skills versions review`, `$akasha skills install <id>`. Команды используют текущую группу подключённого проекта либо явно указанную `--space`.

Публикация сначала сохраняет пакет локально. `skill-sync` шифрует события и передаёт их в hub; для первого обмена нужны токен hub и локальный vault. На другом компьютере создайте/восстановите ту же группу, перенесите ключ её shared-проекта штатными `key-export`/`key-import`, затем выполните `skill-pull`. Для доступа другим участникам администратор назначает reader/editor на shared-проект группы. Пароль vault и ключ не хранятся внутри skill.

Установка по умолчанию идёт в `$CODEX_HOME/skills/<name>` или `~/.codex/skills/<name>`. Отличающийся установленный skill сохраняется. Явный `--replace` сначала перемещает его в соседний `.akasha-backup-...`; прежние файлы остаются доступны. Содержимое архива проверяется по manifest и хэшам до установки, пути traversal, symlinks, дубли путей и специальные файлы отклоняются. Скрипты не запускаются. После установки обновите список skills Codex/откройте новый чат, если skill ещё не виден.

Лимиты первой реализации: 512 файлов, 4 MiB на файл, 16 MiB распакованного содержимого и 8 MiB ZIP. Файлы с признаками секретов отклоняются; сканер — вспомогательная проверка, не гарантия отсутствия любого секрета. Пакеты не входят в автоматический retrieval контекста. Публикация пользовательского skill не является аудитом его безопасности или лицензии.
