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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/score-spec/score-go/v2/schema"
	"github.com/score-spec/score-go/v2/score"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// DeploymentManifestKey manifest key to hold the Deployment. Always present.
	DeploymentManifestKey = "deployment"
	// SecretManifestKey manifest key to hold a Secret with the workload's secrets. Present only when the workload has any.
	SecretManifestKey = "secret"

	// NameLabel is the label that selects the workload's pods. Its value is metadata.name.
	NameLabel = "app.kubernetes.io/name"

	// SecretChecksumAnnotation is the pod's annotation to hash its Secret's data, to restart the pod when secrets changed.
	SecretChecksumAnnotation = "score.dev/secret-checksum"
)

// Generator generates Kubernetes manifests from a Score workload.
type Generator struct{}

var _ score.Generator = (*Generator)(nil)

// NewGenerator returns a Generator.
func NewGenerator() *Generator {
	return &Generator{}
}

// Generate generates the Kubernetes manifests from the score spec and the substitution map.
// It returns a Deployment and a Secret (if the workload has secret values), both named after metadata.name.
func (g *Generator) Generate(_ context.Context, spec *score.Workload, substitutions score.Substitutions) (map[string][]byte, error) {
	if spec == nil {
		return nil, fmt.Errorf("no workload to generate Kubernetes manifests from")
	}
	// The Score schema is at least as strict as Kubernetes for the fields we use (metadata.name, container
	// names, probe ports, resource quantities), so a valid spec produces a valid Deployment.
	if err := schema.ValidateSpec(spec); err != nil {
		return nil, fmt.Errorf("invalid score spec: %w", err)
	}
	name, _ := spec.Metadata["name"].(string) // required and a string per the schema

	manifests := map[string]any{}

	b := &builder{workload: *spec, substitutions: substitutions, name: name}
	podContainers, err := b.containers()
	if err != nil {
		return nil, err
	}

	labels := map[string]string{NameLabel: name}
	podMeta := metav1.ObjectMeta{Labels: labels}

	if len(b.secretData) > 0 {
		manifests[SecretManifestKey] = &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
			Type:       corev1.SecretTypeOpaque,
			Data:       b.secretData,
		}
		checksum, err := secretChecksum(b.secretData)
		if err != nil {
			return nil, err
		}
		podMeta.Annotations = map[string]string{SecretChecksumAnnotation: checksum}
	}

	manifests[DeploymentManifestKey] = &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: podMeta,
				Spec:       corev1.PodSpec{Containers: podContainers},
			},
		},
	}

	out := make(map[string][]byte, len(manifests))
	for key, manifest := range manifests {
		if out[key], err = json.Marshal(manifest); err != nil {
			return nil, fmt.Errorf("serializing the %s manifest to json: %w", key, err)
		}
	}
	return out, nil
}

// secretChecksum returns a hash of the Secret data. json.Marshal sorts map keys, so it is stable.
func secretChecksum(data map[string][]byte) (string, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("hashing the secret data: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
