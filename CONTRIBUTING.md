# Contributing

Pull requests are welcome.

## Adding a New Algorithm

1. Create `<algorithm>.go` implementing the `Limiter` interface
2. Add a constructor `New<Algorithm>(rate int, window time.Duration) Limiter`
3. Add benchmark and unit tests in `<algorithm>_test.go`
4. Update the comparison table in README.md

## Running Tests

```bash
go test ./...
go test -bench=. -benchmem ./...
```

## Code Style

- `gofmt` before committing
- Keep each algorithm in its own file
- No external dependencies
