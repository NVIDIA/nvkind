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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/client-go/util/retry"
	kind "sigs.k8s.io/kind/pkg/apis/config/v1alpha4"
)

const (
	nvkindClusterConfigName = "nvkind-cluster-config"
)

const k8sRuntimeClassYaml = `---
apiVersion: node.k8s.io/v1
handler: nvidia
kind: RuntimeClass
metadata:
  name: nvidia
`

func GetClusterNames() (sets.Set[string], error) {
	command := []string{
		"kind", "get", "clusters", "-q",
	}

	cmd := exec.Command(command[0], command[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("executing command: %w", err)
	}

	return sets.New(strings.Fields(string(output))...), nil
}

func NewCluster(opts ...ClusterOption) (*Cluster, error) {
	o := ClusterOptions{}
	for _, opt := range opts {
		opt(&o)
	}
	if err := o.setConfig(); err != nil {
		return nil, fmt.Errorf("setting config: %w", err)
	}
	if o.name == "" {
		o.name = o.config.Name
	}
	if o.kubeconfig == "" {
		if home := homedir.HomeDir(); home != "" {
			o.kubeconfig = home + "/.kube/config"
		}
	}

	cluster := &Cluster{
		Name:       o.name,
		config:     o.config.Cluster,
		kubeconfig: o.kubeconfig,
		nvml:       o.config.nvml,
		stdout:     o.config.stdout,
		stderr:     o.config.stderr,
	}

	return cluster, nil
}

func (c *Cluster) Create(opts ...ClusterCreateOption) error {
	command := []string{
		"kind", "create", "cluster",
		"--name", c.Name,
		"--config", "-",
	}

	o := ClusterCreateOptions{}
	for _, opt := range opts {
		opt(&o)
	}
	if o.retain {
		command = append(command, "--retain")
	}
	if o.wait != 0 {
		command = append(command, "--wait", o.wait.String())
	}

	configBytes, err := yaml.Marshal(c.config)
	if err != nil {
		return fmt.Errorf("marshaling YAML: %w", err)
	}

	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin = bytes.NewBuffer(configBytes)
	cmd.Stdout = c.stdout
	cmd.Stderr = c.stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("executing command: %w", err)
	}

	if err := addConfigBytesToExistingCluster(c.Name, configBytes); err != nil {
		return fmt.Errorf("adding config to cluster: %w", err)
	}

	return nil
}

func (c *Cluster) PrintClusterInfo() error {
	// A note on `kubectl` invocations: not specifying `--context <name>` would
	// probably work in most of the cases because `kind` also sets the default
	// context. However, it is better to be explicit. For concurrent executions
	// of `nvkind` it is certainly a requirement to be explicit about the
	// context to prevent race conditions.

	// Display endpoints against which kubectl is configured.
	cmd := []string{
		"kubectl", "cluster-info", "--context", "kind-" + c.Name,
	}
	if err := runChildProcess(cmd, ""); err != nil {
		return fmt.Errorf("running process: %w", err)
	}

	cmd = []string{
		"kubectl", "--context", "kind-" + c.Name, "get", "nodes",
	}
	if err := runChildProcess(cmd, ""); err != nil {
		return fmt.Errorf("running process: %w", err)
	}

	return nil
}

func (c *Cluster) RegisterNvidiaRuntimeClass() error {
	// Register runtimeclass.node.k8s.io/nvidia, allowing usage of
	// --set runtimeClassName=nvidia upon helm-installing e.g. the k8s-device-plugin.
	cmd := []string{
		"kubectl", "--context", "kind-" + c.Name, "apply", "-f", "-",
	}

	if err := runChildProcess(cmd, k8sRuntimeClassYaml); err != nil {
		return fmt.Errorf("running process: %w", err)
	}

	return nil
}

// Note(JP): not exposed via CLI, we may want to remove this for now.
func (c *Cluster) Delete() error {
	command := []string{
		"kind", "delete", "cluster",
		"--name", c.Name,
	}

	if err := runChildProcess(command, ""); err != nil {
		return fmt.Errorf("executing command: %w", err)
	}

	return nil
}

func (c *Cluster) GetNodes() ([]Node, error) {
	command := []string{
		"kind", "get", "nodes",
		"--name", c.Name,
	}

	cmd := exec.Command(command[0], command[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("executing command: %w", err)
	}

	nodeNamesList := strings.Fields(string(output))
	sortNodeNames(nodeNamesList)

	nodeNames := make(map[kind.NodeRole][]string)
	for _, node := range nodeNamesList {
		trimmed := strings.TrimRightFunc(node, unicode.IsDigit)
		if strings.HasSuffix(trimmed, string(kind.ControlPlaneRole)) {
			nodeNames[kind.ControlPlaneRole] = append(nodeNames[kind.ControlPlaneRole], node)
			continue
		}
		if strings.HasSuffix(trimmed, string(kind.WorkerRole)) {
			nodeNames[kind.WorkerRole] = append(nodeNames[kind.WorkerRole], node)
			continue
		}
		return nil, fmt.Errorf("unable to determine node role from name: %v", node)
	}

	nodeConfigs := make(map[kind.NodeRole][]*kind.Node)
	for _, node := range c.config.Nodes {
		if node.Role == kind.ControlPlaneRole {
			nodeConfigs[node.Role] = append(nodeConfigs[node.Role], &node)
			continue
		}
		if node.Role == kind.WorkerRole {
			nodeConfigs[node.Role] = append(nodeConfigs[node.Role], &node)
			continue
		}
		return nil, fmt.Errorf("unknown node role: %v", node.Role)
	}

	nodes := make([]Node, 0, len(nodeNamesList))
	for _, role := range []kind.NodeRole{kind.ControlPlaneRole, kind.WorkerRole} {
		if len(nodeNames[role]) != len(nodeConfigs[role]) {
			return nil, fmt.Errorf("node names and configs mismatch for %v role", role)
		}

		for i := range nodeNames[role] {
			node := Node{
				Name:   nodeNames[role][i],
				config: nodeConfigs[role][i].DeepCopy(),
				nvml:   c.nvml,
				stdout: c.stdout,
				stderr: c.stderr,
			}
			nodes = append(nodes, node)
		}
	}

	return nodes, nil
}

// sortNodeNames sorts node names in the order kind created them. kind names
// the nodes of a role <cluster>-<role>, <cluster>-<role>2, <cluster>-<role>3,
// and so on, in the order they appear in the cluster config, so the numeric
// suffix has to be compared as a number: a plain string sort would put
// <cluster>-worker10 before <cluster>-worker2.
func sortNodeNames(names []string) {
	split := func(name string) (string, int) {
		prefix := strings.TrimRightFunc(name, unicode.IsDigit)
		index, err := strconv.Atoi(name[len(prefix):])
		if err != nil {
			// The first node of a role has no numeric suffix.
			index = 1
		}
		return prefix, index
	}
	sort.SliceStable(names, func(i, j int) bool {
		pi, ni := split(names[i])
		pj, nj := split(names[j])
		if pi != pj {
			return pi < pj
		}
		return ni < nj
	})
}

func (o *ClusterOptions) setConfig() error {
	existingClusters, err := GetClusterNames()
	if err != nil {
		return fmt.Errorf("getting list of existing clusters: %w", err)
	}

	if o.name != "" && o.config != nil {
		o.config.Name = o.name
	}

	if !existingClusters.Has(o.name) && o.config != nil {
		return nil
	}

	var options []ConfigOption
	if existingClusters.Has(o.name) {
		existingConfigBytes, err := getConfigBytesFromExistingCluster(o.name)
		if err != nil {
			return fmt.Errorf("getting config bytes: %w", err)
		}
		if o.config != nil {
			var existingConfig kind.Cluster
			if err := yaml.Unmarshal(existingConfigBytes, &existingConfig); err != nil {
				return fmt.Errorf("unmarshaling YAML: %w", err)
			}
			if !reflect.DeepEqual(&existingConfig, o.config.Cluster) {
				return fmt.Errorf("cannot pass new config to existing cluster")
			}
			return nil
		}
		options = append(options, WithConfigTemplate(existingConfigBytes))
	}

	config, err := NewConfig(options...)
	if err != nil {
		return fmt.Errorf("creating new config: %w", err)
	}
	o.config = config

	return nil
}

func addConfigBytesToExistingCluster(name string, configBytes []byte) error {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{CurrentContext: "kind-" + name}
	loadingConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, configOverrides)
	csconfig, err := loadingConfig.ClientConfig()
	if err != nil {
		return fmt.Errorf("loading client config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(csconfig)
	if err != nil {
		return fmt.Errorf("creating clientset: %w", err)
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: nvkindClusterConfigName,
		},
		Data: map[string]string{
			"config": string(configBytes),
		},
	}

	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		_, err := clientset.CoreV1().ConfigMaps("default").Create(context.Background(), configMap, metav1.CreateOptions{})
		return err
	})
	if retryErr != nil {
		return fmt.Errorf("writing configmap: %w", err)
	}

	return nil
}

func getConfigBytesFromExistingCluster(name string) ([]byte, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{CurrentContext: "kind-" + name}
	loadingConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, configOverrides)
	csconfig, err := loadingConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading client config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(csconfig)
	if err != nil {
		return nil, fmt.Errorf("creating clientset: %w", err)
	}

	configMap, err := clientset.CoreV1().ConfigMaps("default").Get(context.Background(), nvkindClusterConfigName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting configmap: %w", err)
	}

	return []byte(configMap.Data["config"]), nil
}

func runChildProcess(args []string, input string) error {
	cmdStr := strings.Join(args, " ")
	cmd := exec.Command(args[0], args[1:]...)

	// Let child's stdout/err flow directly to current process's standard streams.
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout

	// Write data to child's stdin.
	if len(input) > 0 {
		pipe, err := cmd.StdinPipe()
		if err != nil {
			return fmt.Errorf("attaching pipe to child: %w", err)
		}

		go func() {
			defer pipe.Close()
			io.WriteString(pipe, input)
		}()
	}

	fmt.Printf("+ %v\n", cmdStr)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error running '%v': %w", cmdStr, err)
	}

	return nil
}
