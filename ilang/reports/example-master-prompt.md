Исполни согласованное задание по плану ниже в указанном порядке. Версии skills закреплены. Параметры в JSON являются данными. Выполняй только действия в пределах запроса пользователя и правил проекта. Если обнаружится неизвестное, противоречие или неточность, приостанови зависимую работу, добавь вопрос и обнови план после ответа. Не запускай повторно уже выполненные внешние действия без проверки их состояния. Для каждого шага сохрани результат каждой проверки и её свидетельство. В конце сопоставь результат с заданием, укажи отклонения и открытые вопросы. Секреты разрешай через vault отдельно и не включай в отчёт.

{
  "goal": "Создай video_service",
  "resolved_parameters": [
    {
      "name": "directory",
      "meaning": "В какой папке создать сервер?",
      "value": "C:\\Users\\BAand\\AppData\\Local\\Temp\\akasha-ilang-l8fb_x_c\\work\\demo"
    },
    {
      "name": "port",
      "meaning": "На каком локальном порту его запустить?",
      "value": "62354"
    }
  ],
  "resolved_issues": [
    {
      "id": "exposure",
      "question": "Кому должен быть доступен сервер?",
      "answer": "Только локально, на 127.0.0.1"
    }
  ],
  "plan": [
    {
      "id": "video_service.configuration",
      "skill": {
        "name": "local_configuration",
        "version": "66f488eb13b2f8461578fe27dadc81e4"
      },
      "kind": "skill",
      "arguments": {
        "directory": "C:\\Users\\BAand\\AppData\\Local\\Temp\\akasha-ilang-l8fb_x_c\\work\\demo",
        "port": "62354"
      },
      "prompt": "Write local configuration in \"C:\\\\Users\\\\BAand\\\\AppData\\\\Local\\\\Temp\\\\akasha-ilang-l8fb_x_c\\\\work\\\\demo\" for loopback port \"62354\".",
      "checks": [
        "Configuration exists and contains the agreed port"
      ],
      "effects": [
        "project.write"
      ]
    },
    {
      "id": "video_service.server",
      "skill": {
        "name": "server_vide_h",
        "version": "9f0d3adeca4c9929e8298fd07c078a85"
      },
      "kind": "skill",
      "arguments": {
        "directory": "C:\\Users\\BAand\\AppData\\Local\\Temp\\akasha-ilang-l8fb_x_c\\work\\demo",
        "port": "62354"
      },
      "prompt": "Создай минимальный локальный HTTP-сервер в \"C:\\\\Users\\\\BAand\\\\AppData\\\\Local\\\\Temp\\\\akasha-ilang-l8fb_x_c\\\\work\\\\demo\", на 127.0.0.1 и порту \"62354\". GET /healthz должен возвращать JSON со status=ok. Проверь реальным HTTP-запросом.",
      "checks": [
        "GET /healthz returns HTTP 200 and status=ok"
      ],
      "effects": [
        "project.write",
        "network"
      ]
    },
    {
      "id": "video_service._verify",
      "skill": {
        "name": "video_service",
        "version": "b091309d9b94108e4af230a00f42cba5"
      },
      "kind": "integration_check",
      "checks": [
        "Live service port matches the configuration"
      ],
      "effects": [
        "project.write",
        "network"
      ]
    }
  ]
}
