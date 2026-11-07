# codist-model

The parts of [Codist](https://github.com/AbiXnash/codist) that deal with text from outside the code:

- the root package talks to the model server the user runs (Ollama today): it checks the server has the model and
  asks for JSON that follows a schema. Codist contains no model of its own.
- `incident` reads production logs: it finds the Java errors, groups repeats, and keeps both the innermost cause's
  stack frames and the outer exception's.

```go
import "github.com/AbiXnash/codist-model"
import "github.com/AbiXnash/codist-model/incident"

client := model.New("http://localhost:11434", "qwen2.5-coder:14b", 8192)
ok, note := client.Status(ctx)
errs := incident.Parse(logs)
```

```bash
go test ./...
```
