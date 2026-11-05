---
name: Bug report
about: A query failed, returned a wrong result, lost a write, or the build broke
title: ''
labels: bug
assignees: ''
---

## What happened

A clear description of the problem.

## How to reproduce it

The smallest Go program, or the SQL and the DSN, that shows it. For a build problem, the exact `go build` command and its environment (`CGO_ENABLED`, `GOOS`, `GOARCH`, tags).

```go
...
```

## The full error

Paste the complete output, not a paraphrase: the error, the panic and its stack, or the build log.

```
...
```

## Environment

- Module version (`go list -m github.com/liustack/sqlite`):
- `go version`:
- OS and architecture:
- Other SQLite drivers in the same program, if any:
- For the replicator: the store (AWS S3, R2, MinIO, ...) and the `Config` you pass, without credentials:

## Expected

What you expected instead. If the sqlite3 shell or another driver gives the expected result for the same SQL, say which.

---

Not sure it is a bug, or just want to talk it through? Find me on X: [@liustack](https://x.com/liustack).
