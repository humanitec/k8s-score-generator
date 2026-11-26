// Copyright 2026 Ternki GmbH
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Command k8s-score-generator generates Kubernetes manifests from Score workloads.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/score-spec/score-go/v2/score"
	"sigs.k8s.io/yaml"

	k8s "github.com/humanitec/k8s-score-generator"
)

const usage = `k8s-score-generator generates Kubernetes manifests from Score workloads.

Usage:
  k8s-score-generator generate --file score.yaml [--substitutions subs.yaml] [--output yaml|json]

Commands:
  generate  Print the Kubernetes manifests for a Score workload.

Run "k8s-score-generator COMMAND --help" for the flags of a command.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// usageError is an error in how the command was called. It exits with code 2.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usageErrorf(format string, args ...any) error {
	return usageError{msg: fmt.Sprintf(format, args...)}
}

// run runs the command line args and returns the exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}

	var err error
	switch args[0] {
	case "generate":
		err = runGenerate(ctx, args[1:], stdout, stderr)
	case "help", "-h", "-help", "--help":
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	default:
		err = usageErrorf("unknown command %q", args[0])
	}

	var uerr usageError
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.As(err, &uerr):
		_, _ = fmt.Fprintf(stderr, "k8s-score-generator: %v\nRun \"k8s-score-generator --help\" for usage.\n", err)
		return 2
	default:
		_, _ = fmt.Fprintf(stderr, "k8s-score-generator: %v\n", err)
		return 1
	}
}

// newFlagSet returns a flag set for the command that reports parse errors as usage errors.
func newFlagSet(name, synopsis string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: k8s-score-generator %s\n\n%s\n\nFlags:\n", name, synopsis)
		fs.PrintDefaults()
	}
	return fs
}

// parse parses args into fs and rejects positional arguments.
func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError{msg: err.Error()}
	}
	if fs.NArg() > 0 {
		return usageErrorf("%s: unexpected arguments: %s", fs.Name(), strings.Join(fs.Args(), " "))
	}
	return nil
}

func runGenerate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("generate", `Generates the Kubernetes manifests for a Score workload, as YAML documents or as a JSON List.
The manifests are sorted by their key, and can be applied with "kubectl apply -f".

Only ${resources.*} placeholders are resolved. The Score specification must be normalized.

The manifests include Secrets with the workload's secret values, so handle the output as sensitive.`, stderr)
	file := fs.String("file", "", "Score workload file, YAML or JSON (required)")
	subsFile := fs.String("substitutions", "", "substitutions file, YAML or JSON: a map of placeholders, such as resources.db.host, to {value: ...} or {secret: {type: direct, value: ...}}")
	output := fs.String("output", "yaml", "output format: yaml or json")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *file == "" {
		return usageErrorf("generate: --file is required")
	}
	if *output != "yaml" && *output != "json" {
		return usageErrorf("generate: --output must be yaml or json, got %q", *output)
	}

	var workload score.Workload
	if err := readFile(*file, &workload); err != nil {
		return err
	}
	substitutions := score.Substitutions{}
	if *subsFile != "" {
		if err := readFile(*subsFile, &substitutions); err != nil {
			return err
		}
	}

	manifests, err := k8s.NewGenerator().Generate(ctx, &workload, substitutions)
	if err != nil {
		return err
	}
	// Sorted by key so that the output is stable.
	keys := slices.Sorted(maps.Keys(manifests))

	var out bytes.Buffer
	if *output == "yaml" {
		for _, key := range keys {
			doc, err := yaml.JSONToYAML(manifests[key])
			if err != nil {
				return fmt.Errorf("converting the %s manifest to yaml: %w", key, err)
			}
			out.WriteString("---\n")
			out.Write(doc)
		}
	} else {
		list := struct {
			APIVersion string            `json:"apiVersion"`
			Kind       string            `json:"kind"`
			Items      []json.RawMessage `json:"items"`
		}{APIVersion: "v1", Kind: "List"}
		for _, key := range keys {
			list.Items = append(list.Items, manifests[key])
		}
		raw, err := json.Marshal(list)
		if err != nil {
			return fmt.Errorf("serializing the manifests to json: %w", err)
		}
		out.Write(raw)
		out.WriteByte('\n')
	}
	if _, err := stdout.Write(out.Bytes()); err != nil {
		return fmt.Errorf("writing the manifests: %w", err)
	}
	return nil
}

// readFile decodes the YAML or JSON file at path into out.
func readFile(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
