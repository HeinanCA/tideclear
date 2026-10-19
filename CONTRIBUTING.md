# Contributing

Issues and pull requests are welcome.

## Before you open a pull request

```bash
make test    # go test ./... -race -count=1
make vet
make lint    # golangci-lint v2
```

- Write the test first. Unit tests use fake clients and must not touch the network.
- Output changes need updated golden files: `go test ./internal/report -update`.
- Rule data changes follow [docs/rules.md](docs/rules.md). Do not hand-edit `versions.yaml` or
  `k8s-lifecycle.yaml`.
- tideclear stays read-only. A change that sends anything other than `get` or `list` to a
  cluster, or any write call to AWS, will not be merged.
- Keep dependencies to the standard library, cobra, aws-sdk-go-v2 and the client-go tree. Open
  an issue first if you need another one.

Commit messages follow `type: description`, where type is feat, fix, refactor, docs, test,
chore, perf or ci.
