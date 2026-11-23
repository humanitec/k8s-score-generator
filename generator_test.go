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
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestGenerate(t *testing.T) {
	workload := score.Workload{
		ApiVersion: "score.dev/v1b1",
		Metadata:   score.WorkloadMetadata{"name": "app"},
		Containers: score.WorkloadContainers{
			"web": {
				Image:     "nginx:1.27",
				Command:   []string{"nginx"},
				Args:      []string{"-g", "daemon off;"},
				Variables: score.ContainerVariables{"DB_HOST": "${resources.db.host}"},
				Volumes:   score.ContainerVolumes{"/data": {Source: "${resources.data}", ReadOnly: new(true)}},
			},
			"sidecar": {
				Image:     "busybox",
				Variables: score.ContainerVariables{"PASSWORD": "${resources.db.password}"},
			},
		},
		Service: &score.WorkloadService{Ports: score.WorkloadServicePorts{
			"http":    {Port: 80, TargetPort: new(8080)},
			"metrics": {Port: 9090, Protocol: new(score.ServicePortProtocolUDP)},
		}},
		Resources: score.WorkloadResources{"data": {Type: "volume"}},
	}
	dataVolume := score.SubValue{Value: map[string]any{
		"kubernetes": map[string]any{"persistentVolumeClaim": map[string]any{"claimName": "data"}},
	}}
	// generate runs Generate with the given substitutions and decodes the manifests.
	// The Secret and the Service are nil when they are not generated.
	generate := func(subs score.Substitutions) (appsv1.Deployment, *corev1.Secret, *corev1.Service) {
		t.Helper()
		manifests, err := NewGenerator().Generate(t.Context(), &workload, subs)
		require.NoError(t, err)
		var deployment appsv1.Deployment
		require.NoError(t, json.Unmarshal(manifests[DeploymentManifestKey], &deployment))
		var secret *corev1.Secret
		if raw, ok := manifests[SecretManifestKey]; ok {
			require.NoError(t, json.Unmarshal(raw, &secret))
		}
		var service *corev1.Service
		if raw, ok := manifests[ServiceManifestKey]; ok {
			require.NoError(t, json.Unmarshal(raw, &service))
		}
		return deployment, secret, service
	}

	// Generate successfully and check the output
	subs := score.Substitutions{
		"resources.db.password": score.SubValue{Secret: &score.SecretRef{Type: score.SecretTypeDirect, Value: "s3cr3t"}},
		"resources.db.host":     score.SubValue{Value: "hostname"},
		"resources.data":        dataVolume,
	}
	deployment, secret, service := generate(subs)

	labels := map[string]string{NameLabel: "app"}
	assert.Equal(t, "apps/v1", deployment.APIVersion)
	assert.Equal(t, "Deployment", deployment.Kind)
	assert.Equal(t, "app", deployment.Name)
	assert.Equal(t, labels, deployment.Labels)
	assert.Equal(t, labels, deployment.Spec.Selector.MatchLabels)
	assert.Equal(t, labels, deployment.Spec.Template.Labels, "the selector must match the pod labels")
	assert.Equal(t, []corev1.Container{
		{Name: "sidecar", Image: "busybox", Env: []corev1.EnvVar{{
			Name: "PASSWORD",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "app"},
				Key:                  "env.sidecar.PASSWORD",
			}},
		}}},
		{
			Name:    "web",
			Image:   "nginx:1.27",
			Command: []string{"nginx"},
			Args:    []string{"-g", "daemon off;"},
			Env:     []corev1.EnvVar{{Name: "DB_HOST", Value: "hostname"}},
			VolumeMounts: []corev1.VolumeMount{{
				Name:              "data",
				MountPath:         "/data",
				ReadOnly:          true,
				RecursiveReadOnly: new(corev1.RecursiveReadOnlyIfPossible),
			}},
		},
	}, deployment.Spec.Template.Spec.Containers, "containers are sorted by name")
	assert.Equal(t, []corev1.Volume{{
		Name:         "data",
		VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}},
	}}, deployment.Spec.Template.Spec.Volumes)

	require.NotNil(t, secret)
	assert.Equal(t, "v1", secret.APIVersion)
	assert.Equal(t, "Secret", secret.Kind)
	assert.Equal(t, "app", secret.Name, "the variable references the generated Secret")
	assert.Equal(t, labels, secret.Labels)
	assert.Equal(t, corev1.SecretTypeOpaque, secret.Type)
	assert.Equal(t, map[string][]byte{"env.sidecar.PASSWORD": []byte("s3cr3t")}, secret.Data)
	checksum := deployment.Spec.Template.Annotations[SecretChecksumAnnotation]
	assert.NotEmpty(t, checksum)

	require.NotNil(t, service)
	assert.Equal(t, "v1", service.APIVersion)
	assert.Equal(t, "Service", service.Kind)
	assert.Equal(t, "app", service.Name)
	assert.Equal(t, labels, service.Labels)
	assert.Equal(t, labels, service.Spec.Selector, "the selector must match the pod labels")
	assert.Equal(t, []corev1.ServicePort{
		{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8080), Protocol: corev1.ProtocolTCP},
		{Name: "metrics", Port: 9090, TargetPort: intstr.FromInt32(9090), Protocol: corev1.ProtocolUDP},
	}, service.Spec.Ports, "ports are sorted by name, the target port defaults to the port and the protocol to TCP")

	// Generate again with the same substitutions and check that the checksum didn't change
	subs = score.Substitutions{
		"resources.db.password": score.SubValue{Secret: &score.SecretRef{Type: score.SecretTypeDirect, Value: "s3cr3t"}},
		"resources.db.host":     score.SubValue{Value: "hostname"},
		"resources.data":        dataVolume,
	}
	same, _, _ := generate(subs)
	assert.Equal(t, checksum, same.Spec.Template.Annotations[SecretChecksumAnnotation], "the checksum is stable")

	// Generate again with a different secret value and check that the checksum changed
	subs = score.Substitutions{
		"resources.db.password": score.SubValue{Secret: &score.SecretRef{Type: score.SecretTypeDirect, Value: "changed"}},
		"resources.db.host":     score.SubValue{Value: "hostname"},
		"resources.data":        dataVolume,
	}
	changed, _, _ := generate(subs)
	assert.NotEqual(t, checksum, changed.Spec.Template.Annotations[SecretChecksumAnnotation], "a new secret value rolls out new pods")

	// Generate again without secret values and service ports and check that there is no Secret, no checksum and no Service
	workload.Service = nil
	subs = score.Substitutions{
		"resources.db.password": score.SubValue{Value: "not-a-secret"},
		"resources.db.host":     score.SubValue{Value: "hostname"},
		"resources.data":        dataVolume,
	}
	plain, secret, service := generate(subs)
	assert.Nil(t, secret, "no Secret without secret values")
	assert.Empty(t, plain.Spec.Template.Annotations, "no checksum without a Secret")
	assert.Nil(t, service, "no Service without service ports")
}

func TestGenerateErrors(t *testing.T) {
	testCases := []struct {
		name     string
		workload *score.Workload
		wantErr  string
	}{
		{name: "no workload", workload: nil, wantErr: "no workload"},
		{
			name: "invalid metadata.name",
			workload: &score.Workload{
				ApiVersion: "score.dev/v1b1",
				Metadata:   score.WorkloadMetadata{"name": "My_App"},
				Containers: score.WorkloadContainers{"main": {Image: "busybox"}},
			},
			wantErr: "/metadata/name",
		},
		{
			name: "invalid container name",
			workload: &score.Workload{
				ApiVersion: "score.dev/v1b1",
				Metadata:   score.WorkloadMetadata{"name": "app"},
				Containers: score.WorkloadContainers{"Web_1": {Image: "busybox"}},
			},
			wantErr: "/containers",
		},
		{
			name: "probe port out of range",
			workload: &score.Workload{
				ApiVersion: "score.dev/v1b1",
				Metadata:   score.WorkloadMetadata{"name": "app"},
				Containers: score.WorkloadContainers{"main": {
					Image:         "busybox",
					LivenessProbe: &score.ContainerProbe{HttpGet: &score.HttpProbe{Path: "/", Port: 0}},
				}},
			},
			wantErr: "/containers/main/livenessProbe/httpGet/port",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewGenerator().Generate(t.Context(), tc.workload, nil)
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}
