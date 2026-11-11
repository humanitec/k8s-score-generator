# k8s-score-generator

A Go library and command line tool that will turn a [Score](https://score.dev) workload into
Kubernetes manifests, implementing the `score.Generator` interface from
[`github.com/score-spec/score-go/v2/score`](https://github.com/score-spec/score-go/tree/v2).

Work in progress: the generator is not implemented yet.

## Development

```sh
go test -race -cover ./...
golangci-lint run
```

New Go files need the Apache 2.0 license header, which CI checks with
[addlicense](https://github.com/google/addlicense):

```sh
addlicense -l apache -c "Ternki GmbH" -ignore '**/*.yaml' -ignore '**/*.yml' .
```
