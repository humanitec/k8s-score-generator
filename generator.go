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
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/score-spec/score-go/v2/schema"
	"github.com/score-spec/score-go/v2/score"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	// DeploymentManifestKey manifest key to hold the Deployment. Always present.
	DeploymentManifestKey = "deployment"
	// SecretManifestKey manifest key to hold a Secret with the workload's secrets. Present only when the workload has any.
	SecretManifestKey = "secret"
	// ServiceManifestKey manifest key to hold a Service. Present only when the workload has service ports.
	ServiceManifestKey = "service"

	// NameLabel is the label that selects the workload's pods. Its value is metadata.name.
	NameLabel = "app.kubernetes.io/name"

	// ConfigChecksumAnnotation is the pod's annotation to hash its secret values and files, to restart
	// the pod when they changed.
	ConfigChecksumAnnotation = "score.dev/config-checksum"
)

// Generator generates Kubernetes manifests from a Score workload.
type Generator struct{}

var _ score.Generator = (*Generator)(nil)

// NewGenerator returns a Generator.
func NewGenerator() *Generator {
	return &Generator{}
}

// Generate generates the Kubernetes manifests from the score spec and the substitution map.
// It returns (all named after metadata.name):
//   - a Deployment
//   - a Secret, if the workload has secret values
//   - a Service, if the workload has service ports
//
// It also returns a ConfigMap for each container file, or a Secret if the file uses a secret value.
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
	}

	filesData := make(map[string][]byte, len(b.files))
	for volumeName, f := range b.files {
		meta := metav1.ObjectMeta{Name: f.object, Labels: labels}
		filesData[volumeName] = f.content
		if f.secret {
			manifests[volumeName] = &corev1.Secret{
				TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
				ObjectMeta: meta,
				Type:       corev1.SecretTypeOpaque,
				Data:       map[string][]byte{f.key: f.content},
			}
			continue
		}
		configMap := &corev1.ConfigMap{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
			ObjectMeta: meta,
		}
		// data holds UTF-8 strings only, so any other content goes to binaryData.
		if utf8.Valid(f.content) {
			configMap.Data = map[string]string{f.key: string(f.content)}
		} else {
			configMap.BinaryData = map[string][]byte{f.key: f.content}
		}
		manifests[volumeName] = configMap
	}

	if len(b.secretData) > 0 || len(filesData) > 0 {
		checksum, err := configChecksum(b.secretData, filesData)
		if err != nil {
			return nil, err
		}
		podMeta.Annotations = map[string]string{ConfigChecksumAnnotation: checksum}
	}

	if spec.Service != nil && len(spec.Service.Ports) > 0 {
		manifests[ServiceManifestKey] = &corev1.Service{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
			Spec: corev1.ServiceSpec{
				Selector: labels,
				Ports:    servicePorts(spec.Service.Ports),
			},
		}
	}

	manifests[DeploymentManifestKey] = &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: podMeta,
				Spec: corev1.PodSpec{
					Containers: podContainers,
					// Sorted by name so that the output is stable.
					Volumes: slices.SortedFunc(maps.Values(b.volumes), func(x, y corev1.Volume) int {
						return strings.Compare(x.Name, y.Name)
					}),
				},
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

// configChecksum returns a hash of the secret values and the file contents by volume name.
// json.Marshal sorts map keys, so it is stable.
func configChecksum(secretData, filesData map[string][]byte) (string, error) {
	raw, err := json.Marshal([]map[string][]byte{secretData, filesData})
	if err != nil {
		return "", fmt.Errorf("hashing the secret values and files: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// servicePorts converts the Score service ports, sorted by name so that the output is stable.
func servicePorts(ports score.WorkloadServicePorts) []corev1.ServicePort {
	out := make([]corev1.ServicePort, 0, len(ports))
	for name, scorePort := range ports {
		targetPort := scorePort.Port
		if scorePort.TargetPort != nil {
			targetPort = *scorePort.TargetPort
		}
		protocol := corev1.ProtocolTCP
		if scorePort.Protocol != nil {
			protocol = corev1.Protocol(*scorePort.Protocol)
		}
		out = append(out, corev1.ServicePort{
			Name:       name,
			Port:       int32(scorePort.Port),
			TargetPort: intstr.FromInt32(int32(targetPort)),
			Protocol:   protocol,
		})
	}
	slices.SortFunc(out, func(a, b corev1.ServicePort) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}
