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

	"github.com/NVIDIA/go-nvml/pkg/nvml"
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
	// nvidia-container-toolkit v1.19.0 added a CDI hook
	// (`disable-device-node-modification`) that bind-mounts a tmpfs with
	// `ModifyDeviceFiles: 0` over /proc/driver/nvidia/params whenever it
	// injects a GPU into a container. When that hook has run we already
	// have the state we want and the legacy masking dance below is
	// redundant — and the first step (`umount -R /proc/driver/nvidia`)
	// actually fails, because the new hook mounts the params file only,
	// not the parent directory.
	//
	// See NVIDIA/nvidia-container-toolkit PR #927.
	masked, err := n.isModifyDeviceFilesDisabled()
	if err != nil {
		return fmt.Errorf("checking /proc/driver/nvidia/params on %v: %w", n.Name, err)
	}

	if !masked {
		// Legacy toolkit (< v1.19.0) bind-mounted a tmpfs over the whole
		// /proc/driver/nvidia directory, hiding the real params file.
		// Unmount that tmpfs to expose the real file, copy it, flip
		// ModifyDeviceFiles to 0, then bind-mount our copy back.
		err := n.runScript(`
			umount -R /proc/driver/nvidia
			cp /proc/driver/nvidia/params root/gpu-params
			sed -i 's/^ModifyDeviceFiles: 1$/ModifyDeviceFiles: 0/' root/gpu-params
			mount --bind root/gpu-params /proc/driver/nvidia/params
		`)
		if err != nil {
			return fmt.Errorf("masking /proc/driver/nvidia/params on %v: %w", n.Name, err)
		}
	}

	// Masking params only stops the driver from recreating device
	// nodes on demand. The kind worker container still inherits the
	// host's full set of /dev/nvidia* nodes at start, so remove the
	// ones this worker should not see.
	if err := n.removeDeviceNodes(); err != nil {
		return fmt.Errorf("removing device nodes %v: %w", n.Name, err)
	}

	return nil
}

// isModifyDeviceFilesDisabled reports whether /proc/driver/nvidia/params
// inside this kind worker already reads `ModifyDeviceFiles: 0`. That is
// the masked state produced by the `disable-device-node-modification`
// CDI hook added in nvidia-container-toolkit v1.19.0; older toolkits
// leave the file at the driver default (`ModifyDeviceFiles: 1`).
func (n *Node) isModifyDeviceFilesDisabled() (bool, error) {
	cmd := exec.Command("docker", "exec", n.Name, "cat", "/proc/driver/nvidia/params")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("reading /proc/driver/nvidia/params: %w", err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		switch strings.TrimSpace(line) {
		case "ModifyDeviceFiles: 0":
			return true, nil
		case "ModifyDeviceFiles: 1":
			return false, nil
		}
	}
	return false, fmt.Errorf("/proc/driver/nvidia/params did not contain a ModifyDeviceFiles line")
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
		if filepath.Dir(mount.ContainerPath) != "/var/run/nvidia-container-devices" {
			continue
		}
		devices = append(devices, filepath.Base(mount.ContainerPath))
	}

	return devices
}
