/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package nvkind

import (
	_ "embed"
	"io"
	"text/template"
	"time"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	kind "sigs.k8s.io/kind/pkg/apis/config/v1alpha4"
)

//go:embed default-config-template.yaml
var defaultConfigTemplate []byte

//go:embed default-config-values.yaml
var defaultConfigValues []byte

type Config struct {
	*kind.Cluster
	nvml   nvml.Interface
	stdout io.Writer
	stderr io.Writer
}

type Cluster struct {
	Name       string
	config     *kind.Cluster
	kubeconfig string
	nvml       nvml.Interface
	stdout     io.Writer
	stderr     io.Writer
}

type Node struct {
	Name   string
	config *kind.Node
	nvml   nvml.Interface
	stdout io.Writer
	stderr io.Writer
}

type GPUInfo struct {
	Index string
	Name  string
	UUID  string
}

type ConfigOptions struct {
	defaultName        string
	image              string
	nvml               nvml.Interface
	stdout             io.Writer
	stderr             io.Writer
	extraFuncMap       template.FuncMap
	configTemplatePath string
	configTemplate     []byte
	configValuesPath   string
	configValues       []byte
}

type ConfigOption func(*ConfigOptions)

func WithDefaultName(name string) ConfigOption {
	return func(o *ConfigOptions) {
		o.defaultName = name
	}
}

func WithImage(image string) ConfigOption {
	return func(o *ConfigOptions) {
		o.image = image
	}
}

func WithNvml(nvml nvml.Interface) ConfigOption {
	return func(o *ConfigOptions) {
		o.nvml = nvml
	}
}

func WithFuncMap(funcMap template.FuncMap) ConfigOption {
	return func(o *ConfigOptions) {
		o.extraFuncMap = funcMap
	}
}

func WithConfigTemplate[T string | []byte](arg T) ConfigOption {
	return func(o *ConfigOptions) {
		switch arg := any(arg).(type) {
		case string:
			o.configTemplatePath = arg
		case []byte:
			o.configTemplate = arg
		}
	}
}

func WithConfigValues[T string | []byte](arg T) ConfigOption {
	return func(o *ConfigOptions) {
		switch arg := any(arg).(type) {
		case string:
			o.configValuesPath = arg
		case []byte:
			o.configValues = arg
		}
	}
}

func WithOutput(stdout, stderr io.Writer) ConfigOption {
	return func(o *ConfigOptions) {
		o.stdout = stdout
		o.stderr = stderr
	}
}

type ClusterOptions struct {
	name       string
	config     *Config
	kubeconfig string
}

type ClusterOption func(*ClusterOptions)

func WithName(name string) ClusterOption {
	return func(o *ClusterOptions) {
		o.name = name
	}
}

func WithConfig(config *Config) ClusterOption {
	return func(o *ClusterOptions) {
		o.config = config
	}
}

func WithKubeConfig(kubeconfig string) ClusterOption {
	return func(o *ClusterOptions) {
		o.kubeconfig = kubeconfig
	}
}

type ClusterCreateOptions struct {
	retain bool
	wait   time.Duration
}

type ClusterCreateOption func(*ClusterCreateOptions)

func WithRetain() ClusterCreateOption {
	return func(o *ClusterCreateOptions) {
		o.retain = true
	}
}

func WithWait(wait time.Duration) ClusterCreateOption {
	return func(o *ClusterCreateOptions) {
		o.wait = wait
	}
}
