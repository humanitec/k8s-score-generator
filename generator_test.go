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
	"encoding/json"
	"testing"

	"github.com/score-spec/score-go/v2/score"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestGenerateDeployment(t *testing.T) {
	workload := score.Workload{
		Metadata: score.WorkloadMetadata{"name": "app"},
		Containers: score.WorkloadContainers{
			"web":     {Image: "nginx:1.27", Command: []string{"nginx"}, Args: []string{"-g", "daemon off;"}},
			"sidecar": {Image: "busybox"},
		},
	}
	manifests, err := NewGenerator().Generate(t.Context(), &workload, nil)
	require.NoError(t, err)
	require.Len(t, manifests, 1)

	var deployment appsv1.Deployment
	require.NoError(t, json.Unmarshal(manifests[DeploymentManifestKey], &deployment))
	assert.Equal(t, "apps/v1", deployment.APIVersion)
	assert.Equal(t, "Deployment", deployment.Kind)
	assert.Equal(t, "app", deployment.Name)

	labels := map[string]string{NameLabel: "app"}
	assert.Equal(t, labels, deployment.Labels)
	assert.Equal(t, labels, deployment.Spec.Selector.MatchLabels)
	assert.Equal(t, labels, deployment.Spec.Template.Labels, "the selector must match the pod labels")
	assert.Equal(t, []corev1.Container{
		{Name: "sidecar", Image: "busybox"},
		{Name: "web", Image: "nginx:1.27", Command: []string{"nginx"}, Args: []string{"-g", "daemon off;"}},
	}, deployment.Spec.Template.Spec.Containers, "containers are sorted by name")
}

func TestGenerateErrors(t *testing.T) {
	testCases := []struct {
		name     string
		workload *score.Workload
		wantErr  string
	}{
		{name: "no workload", workload: nil, wantErr: "no workload"},
		{
			name:     "no containers",
			workload: &score.Workload{Metadata: score.WorkloadMetadata{"name": "app"}},
			wantErr:  "at least one container",
		},
		{
			name:     "no name",
			workload: &score.Workload{Containers: score.WorkloadContainers{"main": {Image: "busybox"}}},
			wantErr:  "metadata.name",
		},
		{
			name: "name is not a string",
			workload: &score.Workload{
				Metadata:   score.WorkloadMetadata{"name": 42},
				Containers: score.WorkloadContainers{"main": {Image: "busybox"}},
			},
			wantErr: "metadata.name",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewGenerator().Generate(t.Context(), tc.workload, nil)
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}
