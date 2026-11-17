/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
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
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	kind "sigs.k8s.io/kind/pkg/apis/config/v1alpha4"
)

// kindNodeName mirrors how kind names nodes: the first node of a role gets
// no suffix, the n-th node (n >= 2) gets the suffix n.
func kindNodeName(cluster string, role kind.NodeRole, n int) string {
	if n == 1 {
		return fmt.Sprintf("%s-%s", cluster, role)
	}
	return fmt.Sprintf("%s-%s%d", cluster, role, n)
}

// installFakeKind puts a fake `kind` binary first in PATH that answers
// `kind get nodes` with the given node names.
func installFakeKind(t *testing.T, names []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake kind binary is a shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\ncat <<'EOF'\n" + strings.Join(names, "\n") + "\nEOF\n"
	if err := os.WriteFile(filepath.Join(dir, "kind"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// workerIndexFromName returns the 0-based position in the config of the
// worker kind created under the given name.
func workerIndexFromName(t *testing.T, cluster, name string) int {
	t.Helper()
	suffix := strings.TrimPrefix(name, cluster+"-"+string(kind.WorkerRole))
	if suffix == "" {
		return 0
	}
	n, err := strconv.Atoi(suffix)
	if err != nil {
		t.Fatalf("unexpected worker name %q", name)
	}
	return n - 1
}

func TestGetNodesMatchesConfig(t *testing.T) {
	const clusterName = "test"

	testCases := []struct {
		description string
		numWorkers  int
		// gpuWorkers is the number of leading workers that get a GPU; the
		// remaining workers are CPU-only.
		gpuWorkers int
	}{
		{description: "9 GPU workers", numWorkers: 9, gpuWorkers: 9},
		{description: "12 GPU workers", numWorkers: 12, gpuWorkers: 12},
		{description: "8 GPU workers and 3 CPU-only workers", numWorkers: 11, gpuWorkers: 8},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			config := &kind.Cluster{
				Nodes: []kind.Node{{Role: kind.ControlPlaneRole}},
			}
			names := []string{kindNodeName(clusterName, kind.ControlPlaneRole, 1)}
			for i := 0; i < tc.numWorkers; i++ {
				node := kind.Node{Role: kind.WorkerRole}
				if i < tc.gpuWorkers {
					node.ExtraMounts = []kind.Mount{{
						HostPath:      "/dev/null",
						ContainerPath: "/var/run/nvidia-container-devices/" + strconv.Itoa(i),
					}}
				}
				config.Nodes = append(config.Nodes, node)
				names = append(names, kindNodeName(clusterName, kind.WorkerRole, i+1))
			}
			installFakeKind(t, names)

			c := &Cluster{Name: clusterName, config: config}
			nodes, err := c.GetNodes()
			if err != nil {
				t.Fatalf("GetNodes: %v", err)
			}
			if len(nodes) != tc.numWorkers+1 {
				t.Fatalf("got %d nodes, want %d", len(nodes), tc.numWorkers+1)
			}

			for _, node := range nodes {
				if node.config.Role == kind.ControlPlaneRole {
					continue
				}
				i := workerIndexFromName(t, clusterName, node.Name)
				wantGPUs := i < tc.gpuWorkers
				if node.HasGPUs() != wantGPUs {
					t.Errorf("node %s: HasGPUs() = %v, want %v", node.Name, node.HasGPUs(), wantGPUs)
					continue
				}
				if !wantGPUs {
					continue
				}
				devices := node.getNvidiaVisibleDevices()
				if want := []string{strconv.Itoa(i)}; len(devices) != 1 || devices[0] != want[0] {
					t.Errorf("node %s: got devices %v, want %v", node.Name, devices, want)
				}
			}
		})
	}
}
