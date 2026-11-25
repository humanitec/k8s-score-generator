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
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/score-spec/score-go/v2/score"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Platform is the key of the Kubernetes spec in a resource's substitution. A substitution can hold
// specs for several platforms, e.g. kubernetes and google-cloud-run.
const Platform = "kubernetes"

const volumeResourceType = "volume"

// Variable names and file names can contain characters that are not allowed in Secret and ConfigMap keys.
var illegalKeyChars = regexp.MustCompile(`[^-._a-zA-Z0-9]`)

// builder converts a Score workload into Kubernetes objects
type builder struct {
	workload      score.Workload
	substitutions score.Substitutions
	name          string
	secretData    map[string][]byte
	volumes       map[string]corev1.Volume
	files         map[string]containerFile // by volume name
}

// containerFile is a container file. It is stored in its own ConfigMap, or in its own Secret
// if it uses a secret value, and mounted from its own volume.
type containerFile struct {
	container string
	path      string
	volume    corev1.Volume
	object    string // name of the ConfigMap or Secret
	key       string
	content   []byte
	secret    bool
}

// containers converts the Score containers, sorted by name so that the output is stable.
func (b *builder) containers() ([]corev1.Container, error) {
	out := make([]corev1.Container, 0, len(b.workload.Containers))
	for name, c := range b.workload.Containers {
		env, err := b.envVars(name, c.Variables)
		if err != nil {
			return nil, fmt.Errorf("containers.%s.variables.%w", name, err)
		}
		mounts, err := b.volumeMounts(c.Volumes)
		if err != nil {
			return nil, fmt.Errorf("containers.%s.volumes.%w", name, err)
		}
		fileMounts, err := b.fileMounts(name, c.Files)
		if err != nil {
			return nil, fmt.Errorf("containers.%s.files.%w", name, err)
		}
		mounts = append(mounts, fileMounts...)
		slices.SortFunc(mounts, func(a, b corev1.VolumeMount) int {
			return strings.Compare(a.MountPath, b.MountPath)
		})
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
			Env:            env,
			VolumeMounts:   mounts,
			Resources:      resources,
			LivenessProbe:  livenessProbe,
			ReadinessProbe: readinessProbe,
		})
	}
	slices.SortFunc(out, func(a, b corev1.Container) int {
		return strings.Compare(a.Name, b.Name)
	})
	if err := b.addFileVolumes(); err != nil {
		return nil, err
	}
	return out, nil
}

// envVars converts the container variables, with their placeholders replaced, sorted by name.
// A variable that uses a secret placeholder is stored in the Secret as a whole and referenced from it.
func (b *builder) envVars(containerName string, vars score.ContainerVariables) ([]corev1.EnvVar, error) {
	var out []corev1.EnvVar
	for name, value := range vars {
		expanded, secret, err := b.expand(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if !secret {
			out = append(out, corev1.EnvVar{Name: name, Value: expanded})
			continue
		}
		key, err := b.addSecret("env."+containerName+"."+name, expanded)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out = append(out, corev1.EnvVar{
			Name: name,
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: b.name},
				Key:                  key,
			}},
		})
	}
	slices.SortFunc(out, func(a, b corev1.EnvVar) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}

// addSecret stores value in the Secret and returns its key.
func (b *builder) addSecret(name, value string) (string, error) {
	key := illegalKeyChars.ReplaceAllString(name, "_")
	if _, exists := b.secretData[key]; exists {
		return "", fmt.Errorf("secret key %q is already used by another variable", key)
	}
	if b.secretData == nil {
		b.secretData = map[string][]byte{}
	}
	b.secretData[key] = []byte(value)
	return key, nil
}

// fileMounts mounts container files from volumes. Each file creates a volume, named with a hash of the
// container name and the file path.
func (b *builder) fileMounts(containerName string, files score.ContainerFiles) ([]corev1.VolumeMount, error) {
	var out []corev1.VolumeMount
	// In sorted order, so that a potential error is always deterministic.
	for _, target := range slices.Sorted(maps.Keys(files)) {
		f := files[target]
		content, secret, err := b.fileContent(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", target, err)
		}
		// Container names can't contain ":", so the hash input is unambiguous.
		sum := sha256.Sum256([]byte(containerName + ":" + target))
		volumeName := fmt.Sprintf("file-%x", sum[:5])
		file := containerFile{
			container: containerName,
			path:      target,
			volume:    corev1.Volume{Name: volumeName},
			object:    b.name + "-" + volumeName,
			key:       illegalKeyChars.ReplaceAllString(path.Base(target), "_"),
			content:   content,
			secret:    secret,
		}
		var mode *int32
		if f.Mode != nil {
			m, err := strconv.ParseInt(*f.Mode, 8, 32)
			if err != nil || m < 0 || m > 0o777 {
				return nil, fmt.Errorf("%s: mode must be an octal number from 0000 to 0777, got %q", target, *f.Mode)
			}
			mode = new(int32(m))
		}
		if secret {
			file.volume.Secret = &corev1.SecretVolumeSource{SecretName: file.object, DefaultMode: mode}
		} else {
			file.volume.ConfigMap = &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: file.object},
				DefaultMode:          mode,
			}
		}
		if b.files == nil {
			b.files = map[string]containerFile{}
		}
		b.files[volumeName] = file
		out = append(out, corev1.VolumeMount{Name: volumeName, MountPath: target, SubPath: file.key, ReadOnly: true})
	}
	return out, nil
}

// fileContent returns the content of a file and whether it holds a secret. Placeholders in content are
// replaced unless noExpand is set. binaryContent is base64 encoded and has no placeholders.
func (b *builder) fileContent(f score.ContainerFile) ([]byte, bool, error) {
	switch {
	case f.Content != nil && f.NoExpand != nil && *f.NoExpand:
		return []byte(*f.Content), false, nil
	case f.Content != nil:
		expanded, secret, err := b.expand(*f.Content)
		return []byte(expanded), secret, err
	case f.BinaryContent != nil:
		content, err := base64.StdEncoding.DecodeString(*f.BinaryContent)
		if err != nil {
			return nil, false, fmt.Errorf("binaryContent: %w", err)
		}
		return content, false, nil
	default:
		return nil, false, fmt.Errorf("content or binaryContent is required, source is not supported")
	}
}

// addFileVolumes adds the volumes of the container files to the pod. A volume resource could have the same
// name as a file's volume. That is very unlikely, as the name is a hash, but it would mount the wrong volume.
func (b *builder) addFileVolumes() error {
	for _, name := range slices.Sorted(maps.Keys(b.files)) {
		f := b.files[name]
		if _, exists := b.volumes[name]; exists {
			return fmt.Errorf("containers.%s.files.%s: volume name %q is already used by a volume resource", f.container, f.path, name)
		}
		if b.volumes == nil {
			b.volumes = map[string]corev1.Volume{}
		}
		b.volumes[name] = f.volume
	}
	return nil
}

// volumeMounts converts the container volumes into mounts and adds the volumes they mount to the pod.
func (b *builder) volumeMounts(vols score.ContainerVolumes) ([]corev1.VolumeMount, error) {
	var out []corev1.VolumeMount
	for path, v := range vols {
		name, err := b.addVolume(v.Source)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		mount := corev1.VolumeMount{Name: name, MountPath: path}
		if v.Path != nil {
			mount.SubPath = *v.Path
		}
		if v.ReadOnly != nil && *v.ReadOnly {
			mount.ReadOnly = true
			// Also make submounts read-only where the node supports it. Kubernetes accepts this only with readOnly.
			mount.RecursiveReadOnly = new(corev1.RecursiveReadOnlyIfPossible)
		}
		out = append(out, mount)
	}
	return out, nil
}

// addVolume adds the volume to the pod and returns its name.
//
// A volume source must be ${resources.NAME}, with NAME a resource of type volume.
// Its substitution is the whole resource: a value with a kubernetes key holding a
// Kubernetes volume source, for example:
//
//	resources.data:
//	  value:
//	    kubernetes:
//	      persistentVolumeClaim: {claimName: data}
func (b *builder) addVolume(source string) (string, error) {
	placeholders := getAllPlaceholdersInString(source)
	if len(placeholders) != 1 || source != "${"+placeholders[0]+"}" {
		return "", fmt.Errorf("source must be ${resources.NAME}, got %q", source)
	}
	name, ok := strings.CutPrefix(placeholders[0], "resources.")
	if !ok || strings.Contains(name, ".") {
		return "", fmt.Errorf("source must be ${resources.NAME}, got %q", source)
	}
	if res, exists := b.workload.Resources[name]; !exists || res.Type != volumeResourceType {
		return "", fmt.Errorf("source %s must reference a resource of type %s", source, volumeResourceType)
	}
	if _, exists := b.volumes[name]; exists {
		return name, nil
	}

	sub, err := b.substitutions.Resolve(placeholders[0])
	if err != nil {
		return "", err
	}
	value, _ := sub.Value.(map[string]any)
	spec, ok := value[Platform].(map[string]any)
	if !ok {
		return "", fmt.Errorf("substitution for %s must be an object with a %s key holding a volume source", source, Platform)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("substitution for %s: %w", source, err)
	}
	volume := corev1.Volume{Name: name}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&volume.VolumeSource); err != nil {
		return "", fmt.Errorf("substitution for %s: invalid volume source: %w", source, err)
	}
	if b.volumes == nil {
		b.volumes = map[string]corev1.Volume{}
	}
	b.volumes[name] = volume
	return name, nil
}

// expand replaces the ${resources.*} placeholders in s with their substitutions and reports
// whether any of them is a secret. A value that isn't a string is encoded as JSON.
func (b *builder) expand(s string) (string, bool, error) {
	values := map[string]string{}
	secret := false
	for _, placeholder := range getAllPlaceholdersInString(s) {
		sub, err := b.substitutions.Resolve(placeholder)
		if err != nil {
			return "", false, err
		}
		if sub.Secret != nil {
			secret = true
			values[placeholder], err = secretValue(sub.Secret)
		} else {
			values[placeholder], err = toString(sub.Value)
		}
		if err != nil {
			return "", false, fmt.Errorf("placeholder ${%s}: %w", placeholder, err)
		}
	}
	expanded, err := replaceAllPlaceholdersInString(s, values)
	return expanded, secret, err
}

// secretValue returns the value of a secret. Only direct secrets, which carry their value, are supported.
func secretValue(secret *score.SecretRef) (string, error) {
	if secret.Type != score.SecretTypeDirect {
		// For now, we don't support secret references. Secrets are passed in the substitution map as flat values.
		// In the future we may need to support secret references.
		return "", fmt.Errorf("unsupported secret type %q", secret.Type)
	}
	if secret.Value == nil {
		return "", fmt.Errorf("direct secret has no value")
	}
	return toString(secret.Value)
}

// toString returns a string as is, nil as an empty string and any other value encoded as JSON.
func toString(v any) (string, error) {
	switch v := v.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	default:
		b, err := json.Marshal(v)
		return string(b), err
	}
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
