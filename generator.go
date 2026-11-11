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

	"github.com/score-spec/score-go/v2/score"
)

// Generator generates Kubernetes manifests from a Score workload.
type Generator struct{}

var _ score.Generator = (*Generator)(nil)

// NewGenerator returns a Generator.
func NewGenerator() *Generator {
	return &Generator{}
}

// Generate generates the Kubernetes manifests from the score spec and the substitution map.
// It is not implemented yet and returns no manifests.
func (g *Generator) Generate(_ context.Context, _ *score.Workload, _ score.Substitutions) (map[string][]byte, error) {
	return map[string][]byte{}, nil
}
