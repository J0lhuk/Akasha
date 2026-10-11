"""Create an explicitly labelled, isolated local demo via the real CLI."""
import json
import pathlib
import subprocess

ROOT = pathlib.Path(__file__).resolve().parents[1]
EXE = ROOT / "bin" / "akasha.exe"
DATA = ROOT / ".local" / "demo"
SOURCES = ROOT / ".local" / "demo-sources"


def cli(command, *args):
    result = subprocess.run([str(EXE), command, "--data", str(DATA), *map(str, args)], capture_output=True, text=True, encoding="utf-8", check=True)
    return json.loads(result.stdout)


def main():
    cli("init")
    existing = {s["id"] for s in cli("spaces")}
    for ident, title in [("demo-work", "Работа · пример"), ("demo-home", "Личное · пример")]:
        group = SOURCES / ident
        group.mkdir(parents=True, exist_ok=True)
        if ident not in existing:
            cli("space-add", "--id", ident, "--name", title, "--root", group)
    samples = [("demo-atlas", "demo-work", "Atlas API"), ("demo-lab", "demo-home", "Домашняя лаборатория")]
    for ident, space, title in samples:
        path = SOURCES / space / ident
        path.mkdir(exist_ok=True)
        if not (path / ".git").exists():
            subprocess.run(["git", "init", "--quiet", str(path)], check=True)
        if ident not in {p["id"] for p in cli("projects", "--space", space)}:
            cli("project-add", "--id", ident, "--space", space, "--name", title, "--root", path)
    source = SOURCES / "demo-work" / "demo-atlas" / "queue.go"
    if not source.exists():
        source.write_text('package atlas\n\n// RetryDelay returns the confirmed retry policy for a job.\nfunc RetryDelay(attempt int) int {\n    if attempt > 4 { return 0 }\n    return 30\n}\n', encoding="utf-8")
    notes = [
        ("architecture/overview", "Архитектура сервиса", "# Как устроен Atlas\n\nУчебный проект для проверки Akasha. API принимает задания, сохраняет их в PostgreSQL и передаёт исполнителям через NATS JetStream.\n\n## Границы модулей\n\nAPI отвечает за проверку запроса. Планировщик управляет очередью. Исполнитель фиксирует результат и подтверждает сообщение.\n\nПодробности: [[Повторные попытки]], [[Хранение результатов]], [[Контракт API]].\n\n## Решения\n\nКаждый запрос получает idempotency key. Наблюдаемость описана в [[Метрики и диагностика]]."),
        ("architecture/retry", "Повторные попытки", "Повтор задачи выполняется через 30 секунд. Максимум — 4 попытки. Ошибки валидации не повторяются.\n\nРеализация: [[RetryDelay]]. Основание: [[Архитектура сервиса]]."),
        ("architecture/storage", "Хранение результатов", "Подтверждённые результаты хранятся в PostgreSQL. Ключ дедупликации уникален в пределах проекта.\n\nСвязи: [[Архитектура сервиса]], [[Контракт API]]."),
        ("api/contract", "Контракт API", "POST /jobs создаёт задание и возвращает его ID. GET /jobs/{id} читает состояние.\n\nIdempotency-Key обязателен при повторной отправке. Смотрите [[Хранение результатов]]."),
        ("operations/metrics", "Метрики и диагностика", "Следим за размером очереди, временем выполнения и числом повторных попыток.\n\nЕсли очередь растёт, сначала проверить [[Повторные попытки]], затем доступность исполнителей.\n\nНи один графовый ранг не подтверждает истинность факта."),
        ("workflow/handoff", "Продолжение работы", "Последнее подтверждённое состояние: очередь и хранение спроектированы.\n\nСледующий шаг: проверить таймаут API на опубликованном сценарии.\n\nПеред завершением: сохранить [[Архитектура сервиса]], итог проверки и checkpoint."),
    ]
    for entity, title, text in notes:
        cli("remember", "--project", "demo-atlas", "--entity", entity, "--title", title, "--text", text)
    cli("import", "--project", "demo-atlas")
    shared = next(p for p in cli("projects", "--space", "demo-work") if p["shared"])
    cli("remember", "--project", shared["id"], "--entity", "conventions", "--title", "Правила команды", "--text", "Решения сопровождаем источником и датой проверки. Секреты сохраняем только в vault. Перед передачей работы создаём checkpoint.")
    cli("remember", "--project", "demo-lab", "--entity", "home", "--title", "Изолированная личная заметка", "--text", "Эта демонстрационная заметка не должна попадать в контекст рабочего Atlas API.")
    for value, entity in [("15 секунд", "a"), ("30 секунд", "b")]:
        # Only seed alternatives on the first run; production facts are never auto-resolved.
        if not cli("conflicts", "--project", "demo-atlas"):
            current = cli("list", "--project", "demo-atlas", "--kind", "fact")
            if not any(r.get("value") == value for r in current):
                cli("remember", "--project", "demo-atlas", "--kind", "fact", "--title", "Таймаут API — требуется проверка", "--subject", "Atlas API", "--predicate", "таймаут", "--value", value, "--text", "Демонстрация сохранения противоречивых требований.")
    cli("remember", "--project", "demo-atlas", "--kind", "prompt", "--entity", "prompts/demo", "--title", "Пример запроса", "--text", "Проверь политику повторных попыток и собери контекст для исправления таймаута.")
    print(json.dumps({"data": str(DATA), "projects": [p[0] for p in samples], "demo": True}, ensure_ascii=False))


if __name__ == "__main__":
    main()
