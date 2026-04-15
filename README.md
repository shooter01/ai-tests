# test-orch

## Как запускать

### 1. Поднять Ollama и собрать reviewer image

```powershell
cd C:\Users\user\work\test\test-orch
docker compose up -d ollama
docker compose build reviewer
docker compose exec ollama ollama pull qwen2.5-coder:7b
```

### 2. Поставить зависимости Go

```powershell
go mod tidy
```

### 3. Запустить factory

```powershell
$env:DOCKER_NETWORK="test-orch_default"
go run .\cmd\factory
```

Если у тебя compose project называется не `test-orch`, поменяй `DOCKER_NETWORK`.

### 4. Запустить web

В новом окне:

```powershell
go run .\cmd\web
```

### 5. Открыть UI

Открой:

```text
http://localhost:8080/pr/1
```

Нажми кнопку `Run AI Review`.
