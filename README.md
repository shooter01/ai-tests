# test-orch

Автоматическое ревью pull-request'ов в локальной GitVerse (форк Gitea) с помощью GigaChat через opencode.

## Архитектура
GitVerse ──webhook──▶ orchestrator (Go) ──▶ opencode (Docker) ──▶ GigaChat API
│                                             │
└────── коммент с JSON-ревью в PR ◀───────────┘

- **GitVerse** локально на `http://localhost:3000`, публичный API по префиксу `/public/api`.
- **orchestrator (main.go)** — принимает вебхуки, тянет diff, запускает контейнер, постит результат обратно.
- **opencode-giga** — Docker-образ с установленным `opencode-ai` (см. Dockerfile).
- **GigaChat** — внешняя LLM, токен автообновляется через OAuth.

## Требования

- Windows 10/11 + PowerShell 7+ (код рассчитан на Windows-путь маунта `${PWD}:/work`).
- Docker Desktop, запущенный и работающий.
- Go 1.21+.
- Локальный GitVerse на `:3000` с включённым фичафлагом `PublicAPI` для пользователя, от чьего имени будут прилетать PR.
- Аккаунт GigaChat: `client_id` + `client_secret` (они нужны как `GIGACHAT_AUTH_KEY = base64(client_id:client_secret)`).

## Как запускать

### 1. Подготовить образ opencode

```powershell
cd C:\Users\user\work\test\test-orch
docker build -t opencode-giga .
```

Проверка, что собралось:

```powershell
docker run --rm opencode-giga --version
```

### 2. Создать `.env`

Положи в корень проекта рядом с `main.go`:

```dotenv
ADDR=:8081

# GitVerse Public API
GITEA_BASE_URL=http://localhost:3000/public/api
GITEA_TOKEN=<Personal Access Token с правами repository>
GITEA_WEBHOOK_SECRET=

# GigaChat (OAuth — токены обновляются автоматически)
GIGACHAT_AUTH_KEY=<base64(client_id:client_secret)>
GIGACHAT_SCOPE=GIGACHAT_API_PERS

# opencode
OPENCODE_IMAGE=opencode-giga
OPENCODE_MODEL=gigachat/GigaChat-2-Max
OPENCODE_AGENT=review
```

`.env` — в `.gitignore`, секретов в git быть не должно.

### 3. Проверить `opencode.json` и агента

В корне должен быть `opencode.json` (в репозитории есть пример). Ключевое:

```json
"options": {
  "baseURL": "https://gigachat.devices.sberbank.ru/api/v1",
  "apiKey": "{env:GIGACHAT_TOKEN}",
  "headers": { "X-Client-ID": "postman-request-collection" }
}
```

`apiKey` берётся из env — токен подставляет оркестратор. В файле руками ничего менять не надо.

Системная инструкция для ревьюера лежит в `agents/review.md` и подгружается **в Go** при старте процесса (мы не используем opencode-agent из-за особенностей версии).

### 4. Поставить зависимости Go

```powershell
go mod tidy
```

### 5. Запустить оркестратор

```powershell
go run .
```

В логе должны появиться:
.env loaded
review prompt loaded: 5430 bytes from ./agents/review.md
listening on :8081

Если `.env` не нашёлся или `agents/review.md` отсутствует — процесс упадёт, это ожидаемо.

### 6. Настроить вебхук в GitVerse

В репозитории, который хочешь ревьюить: *Settings → Webhooks → Add Webhook → Gitea*:

- **Target URL**: `http://host.docker.internal:8081/hook` если GitVerse в контейнере, или `http://localhost:8081/hook` если GitVerse запущен из IDE на хосте.
- **HTTP Method**: `POST`, **Content Type**: `application/json`.
- **Secret**: тот же, что в `GITEA_WEBHOOK_SECRET` (можно оставить пустым для dev).
- **Trigger**: Pull Request (Custom Events → Pull Request).

### 7. Проверить руками, что API достижим

```powershell
$tok = "<тот же что в GITEA_TOKEN>"
$acc = "application/vnd.gitverse.object+json;version=1"

curl.exe -i "http://localhost:3000/public/api/ping" `
  -H "Authorization: Bearer $tok" `
  -H "Accept: $acc"
```

Ожидается `200 OK`. Если `403` — у пользователя не включён сервис `PublicAPI`, надо включить в админке.

### 8. Триггернуть ревью

Создай PR в репозитории, где настроил webhook. В логах оркестратора должно появиться:
hook accepted: pr=#N repo=owner/name action=opened ...
fetch files: GET http://localhost:3000/public/api/repos/owner/name/pulls/N/files
gigachat token refreshed, expires at ...
opencode stderr: ...

Через 5–30 секунд в PR появится коммент от бота с JSON-отчётом.

## Как проверить работу на тестовом уязвимом коде

В `docs/test-vulnerable.go` лежит минимальный пример с SQLi, command injection и XSS. Скопируй в новый бранч, открой PR — ожидаемо получишь коммент:

```json
{
  "summary": "Найдены критические уязвимости...",
  "issues": [
    {"file": "main.go", "severity": "critical", "category": "sql-injection", ...},
    {"file": "main.go", "severity": "critical", "category": "command-injection", ...},
    {"file": "main.go", "severity": "high", "category": "xss", ...}
  ],
  "verdict": "request_changes"
}
```

## Частые проблемы

### `proxyconnect tcp: dial 127.0.0.1:3129`

GitVerse не может дойти до оркестратора из-за прокси в окружении. В окне, где запущен GitVerse:

```powershell
$env:NO_PROXY = "localhost,127.0.0.1,::1"
```

Перезапусти GitVerse.

### `404 page not found` от оркестратора

В настройках вебхука Target URL без `/hook` на конце.

### `bad signature`

`GITEA_WEBHOOK_SECRET` в `.env` не совпадает с полем *Secret* в настройках вебхука. Либо оба очисти (dev), либо оба приведи к одному значению.

### `files 403` при `fetch files`

У пользователя, чей PAT используется, не включён сервис `PublicAPI`. Включает админ инстанса через админку / SBT-слой.

### `self signed certificate in certificate chain`

TLS к GigaChat. Для dev отключается через `NODE_TLS_REJECT_UNAUTHORIZED=0`, оркестратор сам прокидывает это в контейнер opencode. Если не помогает — кладёшь Russian Trusted Root CA в trust store хоста.

### `Token has expired`

Означает, что opencode получил старый токен. Обычно — оркестратор был перезапущен позже, чем GigaChat-токен в `.env`. В нынешней версии токен обновляется автоматически через `GIGACHAT_AUTH_KEY`. Убедись, что `GIGACHAT_AUTH_KEY` реальный, а не тестовый.

### `hook skipped: event=""`

Форк не шлёт заголовок `X-Gitea-Event`. Оркестратор определяет тип события по форме payload (`pullRequest` → PR, `comment` → issue_comment). Если событие не `pull_request`, оно тихо пропускается, это ок.

### opencode «висит» после `Database migration complete.`

Обычно одно из трёх:
- забыл смонтировать `${PWD}:/work` — opencode не видит `opencode.json`;
- положил пустой промпт как аргумент (в PowerShell `$prompt` — встроенная функция, без явного присвоения она пуста);
- `--pure` / `--print-logs` у некоторых версий переводят CLI в интерактивный режим.

Быстрый прогон «жив или нет»:

```powershell
docker run --rm `
  -e NODE_TLS_REJECT_UNAUTHORIZED=0 `
  -e GIGACHAT_TOKEN=$env:GIGACHAT_TOKEN `
  -v "${PWD}:/work" -w /work `
  opencode-giga `
  run --model gigachat/GigaChat-2 "Скажи ровно: pong"
```

## Структура проекта
.
├── main.go              # оркестратор
├── .env                 # секреты (не в git)
├── opencode.json        # конфиг opencode: provider, model, apiKey из env
├── agents/
│   └── review.md        # system prompt для ревьюера (читается Go на старте)
├── Dockerfile           # сборка opencode-giga
└── README.md

## Переменные окружения (полный список)

| Переменная | По умолчанию | Описание |
|---|---|---|
| `ADDR` | `:8080` | на каком порту слушать вебхуки |
| `GITEA_BASE_URL` | `http://localhost:3000` | база API GitVerse, **с учётом `/public/api`** |
| `GITEA_TOKEN` | — | PAT с правом `repository` (read+write) |
| `GITEA_WEBHOOK_SECRET` | пусто | HMAC-секрет; пусто = проверка выключена |
| `GIGACHAT_AUTH_KEY` | — | base64(client_id:client_secret) |
| `GIGACHAT_SCOPE` | `GIGACHAT_API_PERS` | scope для OAuth |
| `OPENCODE_IMAGE` | `opencode-giga` | имя Docker-образа |
| `OPENCODE_MODEL` | `gigachat/GigaChat-2` | рекомендую `gigachat/GigaChat-2-Max` |
| `OPENCODE_AGENT` | `review` | имя агента (сейчас не используется, оставлено на будущее) |
| `WORKDIR_ON_HOST` | `cwd` | какую папку хоста маунтить в `/work` контейнера |
| `REVIEW_PROMPT_PATH` | `./agents/review.md` | путь к system prompt |