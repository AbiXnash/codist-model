# codist-model

The parts of [Codist](https://github.com/abxengg/codist) that deal with text from outside the code:

- the root package talks to the model server the user runs (Ollama today): it checks the server has the model and
  asks for JSON that follows a schema. Codist contains no model of its own.
- `incident` reads production logs: it finds the Java errors, groups repeats, and keeps both the innermost cause's
  stack frames and the outer exception's.

```go
import "github.com/abxengg/codist-model"
import "github.com/abxengg/codist-model/incident"

client := model.New("http://localhost:11434", "qwen2.5-coder:14b", 8192)
ok, note := client.Status(ctx)
errs := incident.Parse(logs)
```

```bash
go test ./...
```

## Licence

Source available under the [PolyForm Noncommercial License 1.0.0](LICENSE): free for any noncommercial purpose, such
as personal study, research, hobby projects and testing, and for use by charities, schools, public research and
public health bodies and government institutions. **Using it for a business, or in anything sold, needs a commercial
licence** from the author: open an issue at [github.com/abxengg/codist](https://github.com/abxengg/codist/issues)
to ask. Third-party code included in Codist keeps its own licence.

Required Notice: Copyright 2026 Abinash (https://github.com/AbiXnash)
