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
	"fmt"
	"slices"
	"strings"

	"github.com/score-spec/score-go/v2/score"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// containers converts the Score containers, sorted by name so that the output is stable.
func containers(in score.WorkloadContainers) ([]corev1.Container, error) {
	out := make([]corev1.Container, 0, len(in))
	for name, c := range in {
		resources, err := resourceRequirements(c.Resources)
		if err != nil {
			return nil, fmt.Errorf("containers.%s.resources.%w", name, err)
		}
		livenessProbe, err := probe(c.LivenessProbe)
		if err != nil {
			return nil, fmt.Errorf("containers.%s.livenessProbe: %w", name, err)
		}
		readinessProbe, err := probe(c.ReadinessProbe)
		if err != nil {
			return nil, fmt.Errorf("containers.%s.readinessProbe: %w", name, err)
		}
		out = append(out, corev1.Container{
			Name:           name,
			Image:          c.Image,
			Command:        c.Command,
			Args:           c.Args,
			Resources:      resources,
			LivenessProbe:  livenessProbe,
			ReadinessProbe: readinessProbe,
		})
	}
	slices.SortFunc(out, func(a, b corev1.Container) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}

func resourceRequirements(in *score.ContainerResources) (corev1.ResourceRequirements, error) {
	var out corev1.ResourceRequirements
	if in == nil {
		return out, nil
	}
	var err error
	if in.Requests != nil {
		if out.Requests, err = resourceList(in.Requests); err != nil {
			return out, fmt.Errorf("requests.%w", err)
		}
	}
	if in.Limits != nil {
		if out.Limits, err = resourceList(in.Limits); err != nil {
			return out, fmt.Errorf("limits.%w", err)
		}
	}
	return out, nil
}

func resourceList(in *score.ResourcesLimits) (corev1.ResourceList, error) {
	out := corev1.ResourceList{}
	if in.Cpu != nil {
		q, err := resource.ParseQuantity(*in.Cpu)
		if err != nil {
			return nil, fmt.Errorf("cpu: %w", err)
		}
		out[corev1.ResourceCPU] = q
	}
	if in.Memory != nil {
		q, err := resource.ParseQuantity(*in.Memory)
		if err != nil {
			return nil, fmt.Errorf("memory: %w", err)
		}
		out[corev1.ResourceMemory] = q
	}
	return out, nil
}

// probe converts a Score probe. Score supports httpGet and exec probes.
func probe(in *score.ContainerProbe) (*corev1.Probe, error) {
	switch {
	case in == nil:
		return nil, nil
	case in.HttpGet != nil:
		get := in.HttpGet
		action := &corev1.HTTPGetAction{Path: get.Path, Port: intstr.FromInt32(int32(get.Port))}
		if get.Host != nil {
			action.Host = *get.Host
		}
		if get.Scheme != nil {
			action.Scheme = corev1.URIScheme(*get.Scheme)
		}
		for _, h := range get.HttpHeaders {
			action.HTTPHeaders = append(action.HTTPHeaders, corev1.HTTPHeader{Name: h.Name, Value: h.Value})
		}
		return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: action}}, nil
	case in.Exec != nil:
		return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: in.Exec.Command}}}, nil
	default:
		return nil, fmt.Errorf("one of httpGet or exec is required")
	}
}
