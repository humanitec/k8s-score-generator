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

// Package k8s generates Kubernetes manifests from Score workloads.
package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/score-spec/score-go/v2/score"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeploymentManifestKey is the key of the Deployment manifest in the Generator's output.
const DeploymentManifestKey = "deployment"

// NameLabel is the label that selects the workload's pods. Its value is metadata.name.
const NameLabel = "app.kubernetes.io/name"

// Generator generates Kubernetes manifests from a Score workload.
type Generator struct{}

var _ score.Generator = (*Generator)(nil)

// NewGenerator returns a Generator.
func NewGenerator() *Generator {
	return &Generator{}
}

// Generate generates the Kubernetes manifests from the score spec and the substitution map.
// It returns a Deployment, as JSON, named after metadata.name.
func (g *Generator) Generate(_ context.Context, spec *score.Workload, _ score.Substitutions) (map[string][]byte, error) {
	if spec == nil {
		return nil, fmt.Errorf("no workload to generate Kubernetes manifests from")
	}
	if len(spec.Containers) == 0 {
		return nil, fmt.Errorf("a workload needs at least one container")
	}
	name, _ := spec.Metadata["name"].(string)
	if name == "" {
		return nil, fmt.Errorf("a workload needs metadata.name")
	}

	labels := map[string]string{NameLabel: name}
	deployment := appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: containers(spec.Containers)},
			},
		},
	}

	raw, err := json.Marshal(&deployment)
	if err != nil {
		return nil, fmt.Errorf("serializing the deployment to json: %w", err)
	}
	return map[string][]byte{DeploymentManifestKey: raw}, nil
}

// containers converts the Score containers, sorted by name so that the output is stable.
func containers(in score.WorkloadContainers) []corev1.Container {
	out := make([]corev1.Container, 0, len(in))
	for name, c := range in {
		out = append(out, corev1.Container{
			Name:    name,
			Image:   c.Image,
			Command: c.Command,
			Args:    c.Args,
		})
	}
	slices.SortFunc(out, func(a, b corev1.Container) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}
