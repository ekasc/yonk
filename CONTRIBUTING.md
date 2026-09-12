# Contributing to Yonk

Thanks for your interest. Yonk is experimental (pre-alpha), so expect the
protocol and internals to change. Small, focused contributions are the easiest
to review.

## Prerequisites

- Go 1.25 or newer
- For end-to-end testing of isolated jobs: a Linux amd64 machine with KVM,
  `mkfs.ext4`, and cgroup v2. Most of the test suite does **not** need this.
  The executor tests run against a fake Firecracker and need no root or KVM.

## Build

```bash
go build ./...
```

Or build the binaries:

```bash
go build -o bin/yonk ./cmd/yonk
go build -o bin/yonkd ./cmd/yonkd
```

## Checks

Run the same checks CI runs before opening a pull request:

```bash
gofmt -l .        # should print nothing
go vet ./...
go test ./...
go test -race ./...
```

## Development notes

- Match the style, naming, and structure of the surrounding code. Prefer the
  standard library and existing dependencies over new ones; open an issue
  before adding a dependency.
- Keep changes narrow. Avoid unrelated refactors in the same pull request.
- Add or update tests for behavior changes. For security-relevant changes,
  include a test that would have caught the issue.
- Do not weaken validation, error handling, or isolation guarantees to make a
  test pass. If a test seems wrong, explain why in the pull request.

## Submitting changes

1. Fork the repository and create a branch.
2. Make your change and run the checks above.
3. Open a pull request against `main`. Describe what changed and why, and note
   any behavior or protocol changes.
4. Keep the pull request focused; split unrelated changes into separate pull
   requests.

## Security issues

Do not report security problems in a public issue. See [SECURITY.md](SECURITY.md).

## License

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENSE).
