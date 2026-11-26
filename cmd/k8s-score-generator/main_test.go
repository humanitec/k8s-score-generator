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

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

const workloadYAML = `apiVersion: score.dev/v1b1
metadata:
  name: hello-world
containers:
  main:
    image: busybox:latest
    variables:
      DB_HOST: ${resources.db.host}
      DB_PASSWORD: ${resources.db.password}
    files:
      /etc/app/config.yaml:
        content: "host: ${resources.db.host}"
service:
  ports:
    http:
      port: 80
resources:
  db:
    type: postgres
`

const substitutionsYAML = `resources.db.host:
  value: 10.0.0.5
resources.db.password:
  secret:
    type: direct
    value: s3cr3t
`

// writeFile writes content in a temporary directory, converting it to
// JSON in case of a .json extension, and returns its path.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	data := []byte(content)
	if filepath.Ext(name) == ".json" {
		var err error
		data, err = yaml.YAMLToJSON(data)
		require.NoError(t, err)
	}
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

func runArgs(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestGenerate(t *testing.T) {
	testCases := []struct {
		name, workloadFile, substitutionsFile string
		output                                string // --output; empty means the default
		wantJSON                              bool
	}{
		{name: "yaml by default", workloadFile: "score.yaml", substitutionsFile: "subs.yml"},
		{name: "json", workloadFile: "score.json", substitutionsFile: "subs.json", output: "json", wantJSON: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"generate",
				"--file", writeFile(t, tc.workloadFile, workloadYAML),
				"--substitutions", writeFile(t, tc.substitutionsFile, substitutionsYAML),
			}
			if tc.output != "" {
				args = append(args, "--output", tc.output)
			}
			code, stdout, stderr := runArgs(t, args...)
			require.Equal(t, 0, code, stderr)
			assert.Empty(t, stderr)

			var objects []unstructured.Unstructured
			if tc.wantJSON {
				var list unstructured.UnstructuredList
				require.NoError(t, json.Unmarshal([]byte(stdout), &list))
				assert.Equal(t, "List", list.GetKind())
				objects = list.Items
			} else {
				require.True(t, strings.HasPrefix(stdout, "---\n"), stdout)
				for doc := range strings.SplitSeq(strings.TrimPrefix(stdout, "---\n"), "\n---\n") {
					var object unstructured.Unstructured
					require.NoError(t, yaml.Unmarshal([]byte(doc), &object.Object))
					objects = append(objects, object)
				}
			}
			var got []string
			for _, object := range objects {
				got = append(got, object.GetKind()+"/"+object.GetName())
			}
			assert.Equal(t, []string{
				"Deployment/hello-world",
				"ConfigMap/hello-world-file-782352c4e0",
				"Secret/hello-world",
				"Service/hello-world",
			}, got, "all manifests, sorted by their key")

			host, _, _ := unstructured.NestedString(objects[1].Object, "data", "config.yaml")
			assert.Equal(t, "host: 10.0.0.5", host, "the substitutions are read")
		})
	}
}

func TestUsageErrors(t *testing.T) {
	workload := writeFile(t, "score.yaml", workloadYAML)

	testCases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "no command", args: nil, wantErr: "Usage:"},
		{name: "unknown command", args: []string{"deploy"}, wantErr: `unknown command "deploy"`},
		{name: "unknown flag", args: []string{"generate", "--nope"}, wantErr: "flag provided but not defined: -nope"},
		{name: "positional arguments", args: []string{"generate", "--file", workload, "extra"}, wantErr: "unexpected arguments: extra"},
		{name: "generate without file", args: []string{"generate"}, wantErr: "--file is required"},
		{name: "unknown output format", args: []string{"generate", "--file", workload, "--output", "toml"}, wantErr: `--output must be yaml or json, got "toml"`},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runArgs(t, tc.args...)
			assert.Equal(t, 2, code)
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, tc.wantErr)
		})
	}
}

func TestErrors(t *testing.T) {
	testCases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing file",
			args:    []string{"generate", "--file", filepath.Join(t.TempDir(), "missing.yaml")},
			wantErr: "no such file or directory",
		},
		{
			name:    "invalid workload",
			args:    []string{"generate", "--file", writeFile(t, "score.json", "metadata: {name: x}")},
			wantErr: "score.json",
		},
		{
			name:    "missing substitution",
			args:    []string{"generate", "--file", writeFile(t, "score.yaml", workloadYAML)},
			wantErr: `containers.main.variables.DB_HOST: resolving placeholder "resources.db.host": no substitution found`,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runArgs(t, tc.args...)
			assert.Equal(t, 1, code)
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, tc.wantErr)
		})
	}
}

func TestHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"help"}, {"generate", "--help"}, {"generate", "-h"}} {
		code, _, _ := runArgs(t, args...)
		assert.Equal(t, 0, code, args)
	}
}
