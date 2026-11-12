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
	out, err := containers(score.WorkloadContainers{
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
	})
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
			_, err := containers(score.WorkloadContainers{"main": tc.container})
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}
