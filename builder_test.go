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
	b := &builder{
		name: "app",
		workload: score.Workload{
			Resources: score.WorkloadResources{
				"data":  {Type: "volume"},
				"cache": {Type: "volume"},
			},
			Containers: score.WorkloadContainers{
				"main": {
					Image: "busybox",
					Volumes: score.ContainerVolumes{
						"/data":   {Source: "${resources.data}"},
						"/config": {Source: "${resources.data}", Path: new("config"), ReadOnly: new(true)},
						"/cache":  {Source: "${resources.cache}", ReadOnly: new(false)},
					},
					Files: score.ContainerFiles{
						"/etc/app/config.yaml":  {Content: new("host: ${resources.db.host}"), Mode: new("0644")},
						"/etc/app/secret.yaml":  {Content: new("password: ${resources.db.password}"), Mode: new("0600")},
						"/etc/app/template.txt": {Content: new("password: ${resources.db.password}"), NoExpand: new(true)},
						"/bin/tool":             {BinaryContent: new("AAE=")},
					},
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
			},
		},
		substitutions: score.Substitutions{
			"resources.data":        {Value: map[string]any{"kubernetes": map[string]any{"persistentVolumeClaim": map[string]any{"claimName": "data"}}}},
			"resources.cache":       {Value: map[string]any{"kubernetes": map[string]any{"emptyDir": map[string]any{}}}},
			"resources.db.host":     {Value: "10.0.0.5"},
			"resources.db.password": {Secret: &score.SecretRef{Type: score.SecretTypeDirect, Value: "s3cr3t"}},
		},
	}
	out, err := b.containers()
	require.NoError(t, err)
	assert.Equal(t, []corev1.Container{
		{
			Name:  "main",
			Image: "busybox",
			VolumeMounts: []corev1.VolumeMount{
				{Name: "file-2031ebf787", MountPath: "/bin/tool", SubPath: "tool", ReadOnly: true},
				{Name: "cache", MountPath: "/cache"},
				{Name: "data", MountPath: "/config", SubPath: "config", ReadOnly: true, RecursiveReadOnly: new(corev1.RecursiveReadOnlyIfPossible)},
				{Name: "data", MountPath: "/data"},
				{Name: "file-782352c4e0", MountPath: "/etc/app/config.yaml", SubPath: "config.yaml", ReadOnly: true},
				{Name: "file-4abb8b2beb", MountPath: "/etc/app/secret.yaml", SubPath: "secret.yaml", ReadOnly: true},
				{Name: "file-6f9f7b4538", MountPath: "/etc/app/template.txt", SubPath: "template.txt", ReadOnly: true},
			},
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
	}, out, "containers and volume mounts are sorted")
	assert.Equal(t, map[string]corev1.Volume{
		"cache": {Name: "cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		"data": {Name: "data", VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"},
		}},
		"file-2031ebf787": {Name: "file-2031ebf787", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: "app-file-2031ebf787"},
		}}},
		"file-782352c4e0": {Name: "file-782352c4e0", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: "app-file-782352c4e0"},
			DefaultMode:          new(int32(0o644)),
		}}},
		"file-4abb8b2beb": {Name: "file-4abb8b2beb", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName:  "app-file-4abb8b2beb",
			DefaultMode: new(int32(0o600)),
		}}},
		"file-6f9f7b4538": {Name: "file-6f9f7b4538", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: "app-file-6f9f7b4538"},
		}}},
	}, b.volumes, "a volume mounted twice is added once, each file has its own volume")
	contents := map[string]string{}
	secrets := map[string]bool{}
	for _, f := range b.files {
		contents[f.object+"/"+f.key] = string(f.content)
		secrets[f.object] = f.secret
	}
	assert.Equal(t, map[string]string{
		"app-file-2031ebf787/tool":         "\x00\x01",
		"app-file-782352c4e0/config.yaml":  "host: 10.0.0.5",
		"app-file-4abb8b2beb/secret.yaml":  "password: s3cr3t",
		"app-file-6f9f7b4538/template.txt": "password: ${resources.db.password}",
	}, contents, "each file is stored under its base name")
	assert.Equal(t, map[string]bool{
		"app-file-2031ebf787": false,
		"app-file-782352c4e0": false,
		"app-file-4abb8b2beb": true,
		"app-file-6f9f7b4538": false,
	}, secrets, "only a file with a secret value is stored in a Secret")
	assert.Empty(t, b.secretData, "files are not stored in the variables' Secret")
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
			name:      "file without inline content",
			container: score.Container{Files: score.ContainerFiles{"/etc/app.conf": {Source: new("app.conf")}}},
			wantErr:   "containers.main.files./etc/app.conf: content or binaryContent is required, source is not supported",
		},
		{
			name:      "file with invalid binary content",
			container: score.Container{Files: score.ContainerFiles{"/bin/tool": {BinaryContent: new("not base64")}}},
			wantErr:   "containers.main.files./bin/tool: binaryContent:",
		},
		{
			name:      "file content with missing substitution",
			container: score.Container{Files: score.ContainerFiles{"/etc/app.conf": {Content: new("${resources.db.host}")}}},
			wantErr:   `containers.main.files./etc/app.conf: resolving placeholder "resources.db.host": no substitution found`,
		},
		{
			name:      "file mode not octal",
			container: score.Container{Files: score.ContainerFiles{"/etc/app.conf": {Content: new("x"), Mode: new("0999")}}},
			wantErr:   `containers.main.files./etc/app.conf: mode must be an octal number from 0000 to 0777, got "0999"`,
		},
		{
			name:      "file mode too large",
			container: score.Container{Files: score.ContainerFiles{"/etc/app.conf": {Content: new("x"), Mode: new("01000")}}},
			wantErr:   `mode must be an octal number from 0000 to 0777, got "01000"`,
		},
		{
			name: "file volume name used by a volume resource",
			container: score.Container{
				Volumes: score.ContainerVolumes{"/data": {Source: "${resources.file-3e43d1ed18}"}},
				Files:   score.ContainerFiles{"/etc/app.conf": {Content: new("x")}},
			},
			wantErr: `containers.main.files./etc/app.conf: volume name "file-3e43d1ed18" is already used by a volume resource`,
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
			b := &builder{
				workload: score.Workload{
					Containers: score.WorkloadContainers{"main": tc.container},
					Resources:  score.WorkloadResources{"file-3e43d1ed18": {Type: "volume"}},
				},
				substitutions: score.Substitutions{
					"resources.file-3e43d1ed18": {Value: map[string]any{"kubernetes": map[string]any{"emptyDir": map[string]any{}}}},
				},
			}
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

func TestVolumeMountsErrors(t *testing.T) {
	resources := score.WorkloadResources{
		"data":        {Type: "volume"},
		"db":          {Type: "postgres"},
		"missing":     {Type: "volume"},
		"no-platform": {Type: "volume"},
		"not-object":  {Type: "volume"},
		"typo":        {Type: "volume"},
	}
	substitutions := score.Substitutions{
		"resources.data":        {Value: map[string]any{"kubernetes": map[string]any{"emptyDir": map[string]any{}}}},
		"resources.db":          {Value: map[string]any{}},
		"resources.no-platform": {Value: map[string]any{"google-cloud-run": map[string]any{"emptyDir": map[string]any{}}}},
		"resources.not-object":  {Value: "pvc-data"},
		"resources.typo":        {Value: map[string]any{"kubernetes": map[string]any{"persistentVolumeClam": map[string]any{}}}},
	}
	testCases := []struct {
		name    string
		source  string
		wantErr string
	}{
		{name: "not a placeholder", source: "data", wantErr: `containers.main.volumes./mnt: source must be ${resources.NAME}, got "data"`},
		{name: "text around the placeholder", source: "${resources.data}/sub", wantErr: "source must be ${resources.NAME}"},
		{name: "resource output", source: "${resources.data.path}", wantErr: "source must be ${resources.NAME}"},
		{name: "not a resources placeholder", source: "${metadata.name}", wantErr: "source must be ${resources.NAME}"},
		{name: "unknown resource", source: "${resources.unknown}", wantErr: "source ${resources.unknown} must reference a resource of type volume"},
		{name: "not a volume resource", source: "${resources.db}", wantErr: "source ${resources.db} must reference a resource of type volume"},
		{name: "missing substitution", source: "${resources.missing}", wantErr: `resolving placeholder "resources.missing": no substitution found`},
		{name: "no kubernetes key", source: "${resources.no-platform}", wantErr: "must be an object with a kubernetes key holding a volume source"},
		{name: "substitution not an object", source: "${resources.not-object}", wantErr: "must be an object with a kubernetes key holding a volume source"},
		{name: "unknown field", source: "${resources.typo}", wantErr: `invalid volume source: json: unknown field "persistentVolumeClam"`},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			b := &builder{
				workload: score.Workload{
					Resources:  resources,
					Containers: score.WorkloadContainers{"main": {Volumes: score.ContainerVolumes{"/mnt": {Source: tc.source}}}},
				},
				substitutions: substitutions,
			}
			_, err := b.containers()
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}
