# k8s-score-generator

A Go library and command line tool that turn a [Score](https://score.dev) workload into Kubernetes manifests: a
`Deployment`, and the `Service`, `Secret` and `ConfigMap` objects it needs.

`k8s.Generator` implements the `score.Generator` interface from
[`github.com/score-spec/score-go/v2/score`](https://github.com/score-spec/score-go/tree/v2), so it
can be combined with any `score.Normalizer` and `score.Provisioner`.

## Installation

As a library:

```sh
go get github.com/humanitec/k8s-score-generator@latest
```

As a command line tool:

```sh
go install github.com/humanitec/k8s-score-generator/cmd/k8s-score-generator@latest
```

The package is named `k8s`, not after the module path, so you refer to it as `k8s.Generator`.

## Usage

Given a Score file:

```yaml
apiVersion: score.dev/v1b1
metadata:
  name: hello-world
containers:
  main:
    image: nginx:1.27
    variables:
      DB_HOST: ${resources.db.host}
      DB_PASSWORD: ${resources.db.password}
    files:
      /etc/app/config.yaml:
        content: |
          db:
            host: ${resources.db.host}
    volumes:
      /data:
        source: ${resources.data}
service:
  ports:
    http:
      port: 80
resources:
  db:
    type: postgres
  data:
    type: volume
```

load it, provide substitutions for its `${resources.*}` placeholders, and generate the manifests:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/score-spec/score-go/v2/score"
	"sigs.k8s.io/yaml"

	k8s "github.com/humanitec/k8s-score-generator"
)

func main() {
	src, err := os.ReadFile("score.yaml")
	if err != nil {
		log.Fatal(err)
	}
	var workload score.Workload
	if err := yaml.Unmarshal(src, &workload); err != nil {
		log.Fatal(err)
	}

	// Substitutions normally come from your score.Provisioner: one entry per ${resources.*} placeholder.
	substitutions := score.Substitutions{
		"resources.db.host":     {Value: "10.0.0.5"},
		"resources.db.password": {Secret: &score.SecretRef{Type: score.SecretTypeDirect, Value: "s3cr3t"}},
		"resources.data": {Value: map[string]any{
			"kubernetes": map[string]any{"persistentVolumeClaim": map[string]any{"claimName": "hello-world-data"}},
		}},
	}

	var gen score.Generator = k8s.NewGenerator()
	manifests, err := gen.Generate(context.Background(), &workload, substitutions)
	if err != nil {
		log.Fatal(err)
	}
	for key, manifest := range manifests {
		fmt.Printf("%s: %s\n", key, manifest)
	}
}
```

This generates:

- a `Deployment` with `DB_HOST` set as a plain value, `DB_PASSWORD` referenced from the `Secret`,
  the volume `data` mounted at `/data`, and the file mounted at `/etc/app/config.yaml`.
- a `Secret` with the value of `DB_PASSWORD`.
- a `Service` that forwards port 80 to the pods.
- a `ConfigMap` with the content of `/etc/app/config.yaml`, with `${resources.db.host}` replaced.

### Manifests

`Generate` returns a map from manifest key to a manifest as JSON. All objects have no namespace
and are labeled with `app.kubernetes.io/name: <metadata.name>` (`k8s.NameLabel`).

| Key | Object | Name | Present |
|---|---|---|---|
| `k8s.DeploymentManifestKey` (`deployment`) | `Deployment` | `<metadata.name>` | always |
| `k8s.SecretManifestKey` (`secret`) | `Secret` with the variables' secret values | `<metadata.name>` | when a variable uses a secret |
| `k8s.ServiceManifestKey` (`service`) | `Service` | `<metadata.name>` | when the workload has service ports |
| `file-<hash>` | `ConfigMap`, or `Secret` if the file uses a secret | `<metadata.name>-file-<hash>` | one per container file |

`<hash>` is a hash of the container name and the file path, so the manifest keys of files are
stable across runs. Iterate over the map to get all manifests.

The `Deployment`'s pod selector is the name label. The `Service` selects the same pods.

### Substitutions

Each key in `score.Substitutions` is a placeholder expression without the surrounding `${}`, for
example `resources.db.host`. Only `resources.*` placeholders are resolved. Replace any other
placeholders, such as `${metadata.name}`, before calling `Generate` (it should be a part of
normalization). A placeholder without a substitution is an error.

A `score.SubValue` holds either:

- `Value`: a plain value. Values that aren't strings are encoded as JSON.
- `Secret`: a secret. Only `score.SecretTypeDirect` is supported for now, with the secret value in
  `SecretRef.Value`.

### Variables

A variable without secret placeholders is set as a plain value in the `Deployment`. A variable that
uses at least one secret placeholder is stored as a whole in the workload's `Secret`, under the key
`env.<container>.<variable>`, and referenced through `secretKeyRef`. Characters that aren't allowed
in `Secret` keys are replaced with `_`.

The `Secret` holds the secret values in plain text (base64 encoded), so treat the generated
manifests as sensitive.

### Volumes

A volume source must be `${resources.NAME}`, where `NAME` is a resource of type `volume`. Its
substitution is the whole resource: a value with a `kubernetes` key (`k8s.Platform`) holding a
Kubernetes volume source. For example, to mount a `PersistentVolumeClaim`:

```go
substitutions["resources.data"] = score.SubValue{Value: map[string]any{
	"kubernetes": map[string]any{
		"persistentVolumeClaim": map[string]any{"claimName": "hello-world-data"},
	},
}}
```

### Files

Each file is stored in its own `ConfigMap`, or in its own `Secret` if its content uses a secret
placeholder, and mounted from its own volume with `subPath`. Only the file itself is mounted, so
the other files in its directory, for example the ones from the image, stay available.

- `content` is used with its placeholders replaced, unless `noExpand` is `true`.
- `binaryContent` is decoded from base64 and used as is. Content that isn't UTF-8 is stored in the
  `ConfigMap`'s `binaryData`.
- `mode` sets the file mode, as an octal string from `0000` to `0777`. The default is `0644`.
- `source` is not supported: a normalized workload should have all files inlined.

The key in the `ConfigMap` or `Secret` is the file's base name, with characters that aren't allowed
in keys replaced with `_`.

### Restarts

Files mounted with `subPath` and environment variables don't change in running pods when their
`ConfigMap` or `Secret` changes. Instead, the pod template has the annotation
`score.dev/config-checksum` (`k8s.ConfigChecksumAnnotation`), a hash of all secret values and file
contents. When any of them change, the annotation changes and the `Deployment` rolls out new pods.

## Deploying

The manifests can be applied with `kubectl apply`, or passed to any tool that deploys Kubernetes
manifests. When the generator stops producing an object, for example because a file was removed or
renamed, the object stays in the cluster. Use your deployment tool's pruning to delete it, for
example with the name label:

```sh
kubectl apply --prune -l app.kubernetes.io/name=hello-world -f manifests/
```

## Command line tool

`k8s-score-generator generate` prints the manifests as YAML documents, or as JSON objects 
with `--output json`, sorted by their manifest key. The workload and the substitutions are
read from YAML or JSON files. The substitutions file maps placeholders to values, in the format of
`score.Substitutions`:

```yaml
resources.db.host:
  value: 10.0.0.5
resources.db.password:
  secret:
    type: direct
    value: s3cr3t
resources.data:
  value:
    kubernetes:
      persistentVolumeClaim:
        claimName: hello-world-data
```

The tool runs no normalizer or provisioner. Only `${resources.*}` placeholders are resolved.

```sh
k8s-score-generator generate --file score.yaml --substitutions subs.yaml > manifests.yaml
kubectl apply -f manifests.yaml
```

The output includes the `Secret`s with the workload's secret values, so handle it as sensitive.

Run `k8s-score-generator --help` or `k8s-score-generator generate --help` for all flags. The tool
exits with code 2 for usage errors and 1 for other errors.
