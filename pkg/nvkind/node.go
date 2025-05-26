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
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/NVIDIA/go-nvlib/pkg/nvml"
	"k8s.io/apimachinery/pkg/util/sets"
)

func (n *Node) HasGPUs() bool {
	return n.getNvidiaVisibleDevices() != nil
}

func (n *Node) InstallContainerToolkit() error {
	err := n.runScript(`
		apt-get update
		apt-get install -y gpg
		curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
		curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | \
			sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' | \
				tee /etc/apt/sources.list.d/nvidia-container-toolkit.list
		apt-get update
		apt-get install -y nvidia-container-toolkit
	`)
	if err != nil {
		return fmt.Errorf("running script on %v: %w", n.Name, err)
	}
	return nil
}

func (n *Node) ConfigureContainerRuntime() error {
	err := n.runScript(`
	    nvidia-ctk runtime configure --runtime=containerd --config-source=command
	    systemctl restart containerd
	`)
	if err != nil {
		return fmt.Errorf("running script on %v: %w", n.Name, err)
	}
	return nil
}

func (n *Node) PatchProcDriverNvidia() error {
	// Unmount the masked /proc/driver/nvidia to allow dynamically generated
	// MIG devices to be discovered
	err := n.runScript(`
		umount -R /proc/driver/nvidia
	`)
	if err != nil {
		return fmt.Errorf("running script on %v: %w", n.Name, err)
	}

	// Make it so that calls into nvidia-smi / libnvidia-ml.so do not attempt
	// to recreate device nodes or reset their permissions if tampered with
	err = n.runScript(`
		cp /proc/driver/nvidia/params root/gpu-params
		sed -i 's/^ModifyDeviceFiles: 1$/ModifyDeviceFiles: 0/' root/gpu-params
		mount --bind root/gpu-params /proc/driver/nvidia/params
	`)
	if err != nil {
		return fmt.Errorf("running script on %v: %w", n.Name, err)
	}

	// Remove the device nodes for all GPUs except those this node has access to
	if err := n.removeDeviceNodes(); err != nil {
		return fmt.Errorf("removing device nodes %v: %w", n.Name, err)
	}

	return nil
}

func (n *Node) GetGPUInfo() ([]GPUInfo, error) {
	command := []string{
		"docker", "exec", n.Name,
		"nvidia-smi", "--query-gpu=index,name,uuid", "--format=csv,noheader",
	}

	cmd := exec.Command(command[0], command[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("executing command: %w", err)
	}

	var gpuInfoList []GPUInfo

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		fields := strings.Split(line, ", ")
		gpuInfo := GPUInfo{
			Index: fields[0],
			Name:  fields[1],
			UUID:  fields[2],
		}
		gpuInfoList = append(gpuInfoList, gpuInfo)
	}

	return gpuInfoList, nil
}

func (n *Node) runScript(script string) error {
	command := []string{
		"docker", "exec", n.Name, "bash", "-c", script,
	}

	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdout = n.stdout
	cmd.Stderr = n.stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("executing command: %w", err)
	}

	return nil
}

// TODO: update to support MIG (and other devices)
func (n *Node) removeDeviceNodes() error {
	visibleDevices := sets.New(n.getNvidiaVisibleDevices()...)
	if visibleDevices.Has("all") {
		return nil
	}

	if ret := n.nvml.Init(); ret != nvml.SUCCESS {
		return fmt.Errorf("running nvml.Init: %w", ret)
	}
	defer func() { _ = n.nvml.Shutdown() }()

	numGPUs, ret := n.nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return fmt.Errorf("running nvml.DeviceGetCount: %w", ret)
	}

	scriptFmt := `
		while umount /dev/nvidia%d; do :; done || true
		rm -rf /dev/nvidia%d
	`

	for i := 0; i < numGPUs; i++ {
		if visibleDevices.Has(strconv.Itoa(i)) {
			continue
		}
		if err := n.runScript(fmt.Sprintf(scriptFmt, i, i)); err != nil {
			return fmt.Errorf("running script on %v: %w", n.Name, err)
		}
	}

	return nil
}

// TODO: add a variant of this for CDI once support is added to kind
func (n *Node) getNvidiaVisibleDevices() []string {
	if n.config.ExtraMounts == nil {
		return nil
	}

	var devices []string
	for _, mount := range n.config.ExtraMounts {
		if mount.HostPath != "/dev/null" {
			continue
		}
		dir := filepath.Dir(mount.ContainerPath)
		deviceID := filepath.Base(mount.ContainerPath)
		switch {
		case strings.HasPrefix(dir, "/var/run/nvidia-container-devices/cdi"):
			cdiKind := strings.TrimPrefix(dir, "/var/run/nvidia-container-devices/cdi/")
			devices = append(devices, cdiKind+"="+deviceID)
		case dir == "/var/run/nvidia-container-devices":
			devices = append(devices, deviceID)
		}
	}

	return devices
}
