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

package k8s

import (
	"testing"

	"github.com/score-spec/score-go/v2/score"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestContainers(t *testing.T) {
	https := score.HttpProbeScheme("HTTPS")
	b := &builder{workload: score.Workload{Containers: score.WorkloadContainers{
		"main": {
			Image: "busybox",
			Resources: &score.ContainerResources{
				Requests: &score.ResourcesLimits{Cpu: new("250m"), Memory: new("64Mi")},
				Limits:   &score.ResourcesLimits{Memory: new("128Mi")},
			},
			LivenessProbe: &score.ContainerProbe{HttpGet: &score.HttpProbe{
				Path:        "/healthz",
				Port:        8080,
				Host:        new("localhost"),
				Scheme:      &https,
				HttpHeaders: []score.HttpProbeHttpHeadersElem{{Name: "X-Probe", Value: "1"}},
			}},
			ReadinessProbe: &score.ContainerProbe{Exec: &score.ExecProbe{Command: []string{"cat", "/tmp/ready"}}},
		},
		"minimal": {Image: "nginx"},
	}}}
	out, err := b.containers()
	require.NoError(t, err)
	assert.Equal(t, []corev1.Container{
		{
			Name:  "main",
			Image: "busybox",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("250m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
				Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
			},
			LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
				Path:        "/healthz",
				Port:        intstr.FromInt32(8080),
				Host:        "localhost",
				Scheme:      corev1.URISchemeHTTPS,
				HTTPHeaders: []corev1.HTTPHeader{{Name: "X-Probe", Value: "1"}},
			}}},
			ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
				Command: []string{"cat", "/tmp/ready"},
			}}},
		},
		{Name: "minimal", Image: "nginx"},
	}, out)
}

func TestContainersErrors(t *testing.T) {
	testCases := []struct {
		name      string
		container score.Container
		wantErr   string
	}{
		{
			name:      "invalid cpu request",
			container: score.Container{Resources: &score.ContainerResources{Requests: &score.ResourcesLimits{Cpu: new("lots")}}},
			wantErr:   "containers.main.resources.requests.cpu:",
		},
		{
			name:      "invalid memory limit",
			container: score.Container{Resources: &score.ContainerResources{Limits: &score.ResourcesLimits{Memory: new("1 GB")}}},
			wantErr:   "containers.main.resources.limits.memory:",
		},
		{
			name:      "liveness probe without handler",
			container: score.Container{LivenessProbe: &score.ContainerProbe{}},
			wantErr:   "containers.main.livenessProbe: one of httpGet or exec is required",
		},
		{
			name:      "readiness probe without handler",
			container: score.Container{ReadinessProbe: &score.ContainerProbe{}},
			wantErr:   "containers.main.readinessProbe: one of httpGet or exec is required",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			b := &builder{workload: score.Workload{Containers: score.WorkloadContainers{"main": tc.container}}}
			_, err := b.containers()
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestEnvVars(t *testing.T) {
	b := &builder{name: "app", substitutions: score.Substitutions{
		"resources.db.host":     {Value: "10.0.0.5"},
		"resources.db.port":     {Value: 5432},
		"resources.db.tags":     {Value: map[string]any{"env": "dev"}},
		"resources.db.none":     {},
		"resources.db.user":     {Value: "admin"},
		"resources.db.password": {Secret: &score.SecretRef{Type: score.SecretTypeDirect, Value: "s3cr3t"}},
		"resources.db.pin":      {Secret: &score.SecretRef{Type: score.SecretTypeDirect, Value: 1234}},
	}}
	env, err := b.envVars("main", score.ContainerVariables{
		"PLAIN":   "plain",
		"HOST":    "${resources.db.host}",
		"URL":     "postgres://${resources.db.host}:${resources.db.port}/db",
		"TAGS":    "${resources.db.tags}",
		"EMPTY":   "${resources.db.none}",
		"ESCAPED": "$${resources.db.password}",
		"DSN":     "${resources.db.user}:${resources.db.password}@${resources.db.host}",
		"DB PIN":  "${resources.db.pin}",
	})
	require.NoError(t, err)

	secretRef := func(key string) *corev1.EnvVarSource {
		return &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "app"},
			Key:                  key,
		}}
	}
	assert.Equal(t, []corev1.EnvVar{
		{Name: "DB PIN", ValueFrom: secretRef("env.main.DB_PIN")},
		{Name: "DSN", ValueFrom: secretRef("env.main.DSN")},
		{Name: "EMPTY", Value: ""},
		{Name: "ESCAPED", Value: "${resources.db.password}"},
		{Name: "HOST", Value: "10.0.0.5"},
		{Name: "PLAIN", Value: "plain"},
		{Name: "TAGS", Value: `{"env":"dev"}`},
		{Name: "URL", Value: "postgres://10.0.0.5:5432/db"},
	}, env)
	assert.Equal(t, map[string][]byte{
		"env.main.DB_PIN": []byte("1234"),
		"env.main.DSN":    []byte("admin:s3cr3t@10.0.0.5"),
	}, b.secretData, "a variable with a secret placeholder is stored as a whole")
}

func TestEnvVarsErrors(t *testing.T) {
	substitutions := score.Substitutions{
		"resources.db.password": {Secret: &score.SecretRef{Type: score.SecretTypeDirect, Value: "s3cr3t"}},
		"resources.db.empty":    {Secret: &score.SecretRef{Type: score.SecretTypeDirect, Value: nil}},
		"resources.db.vault":    {Secret: &score.SecretRef{Type: score.SecretTypeVault, Ref: "secret/db"}},
	}
	testCases := []struct {
		name    string
		vars    score.ContainerVariables
		wantErr string
	}{
		{
			name:    "missing substitution",
			vars:    score.ContainerVariables{"X": "${resources.db.host}"},
			wantErr: `containers.main.variables.X: resolving placeholder "resources.db.host": no substitution found`,
		},
		{
			name:    "not a resources placeholder",
			vars:    score.ContainerVariables{"X": "${metadata.name}"},
			wantErr: `containers.main.variables.X: invalid placeholder: must start with "resources"`,
		},
		{
			name:    "incomplete placeholder",
			vars:    score.ContainerVariables{"X": "${resources.db.host"},
			wantErr: "containers.main.variables.X: incomplete placeholder",
		},
		{
			name:    "unsupported secret type",
			vars:    score.ContainerVariables{"X": "${resources.db.vault}"},
			wantErr: `containers.main.variables.X: placeholder ${resources.db.vault}: unsupported secret type "vault"`,
		},
		{
			name:    "direct secret without value",
			vars:    score.ContainerVariables{"X": "${resources.db.empty}"},
			wantErr: "containers.main.variables.X: placeholder ${resources.db.empty}: direct secret has no value",
		},
		{
			name:    "secret key collision",
			vars:    score.ContainerVariables{"A B": "${resources.db.password}", "A_B": "${resources.db.password}"},
			wantErr: `secret key "env.main.A_B" is already used by another variable`,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			b := &builder{
				workload:      score.Workload{Containers: score.WorkloadContainers{"main": {Variables: tc.vars}}},
				substitutions: substitutions,
			}
			_, err := b.containers()
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}
