#!/usr/bin/env bash

# Copyright 2024 NVIDIA CORPORATION.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# A reference to the current directory where this script is located
COMMON_DIR="$(cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd)"

KIND_IMAGE="kindest/node:v1.29.2"
DEFAULT_CLUSTER_CONFIG_PATH=${COMMON_DIR}/cluster-config.yaml
NUM_GPUS="$(nvidia-smi --query-gpu=name --format=csv,noheader | wc -l)"

# Create a kind cluster from a config template
function create_cluster() {
	local kind_image="${1}"
	local cluster_name="${2}"
	local cluster_config_path="${3}"
	local num_workers="${4}"

	cat ${cluster_config_path} | \
		docker run -i -e NUM_WORKERS=${num_workers} hairyhenderson/gomplate | \
			kind create cluster \
				--retain \
				--image "${kind_image}" \
				--name "${cluster_name}" \
				--config -
}

# Delete a kind cluster 
function delete_cluster() {
	local cluster_name="${1}"
	kind delete cluster --name "${cluster_name}"
}

# Install the nvidia-container-toolkit
function install_container_toolkit() {
	local cluster_name="${1}"
	local worker_id="${2}"

	worker="${cluster_name}-worker"
	if [ "${worker_id}" != "1" ]; then
		worker="${worker}${worker_id}"
	fi

	docker exec "${worker}" bash -c "
		apt-get update
		apt-get install -y gpg
	    curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
		curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | \
			sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' | \
				tee /etc/apt/sources.list.d/nvidia-container-toolkit.list
		apt-get update
		apt-get install -y nvidia-container-toolkit
	"
	
}

# We configure the NVIDIA Container Runtime to only trigger on the
# nvidia.cdi.k8s.io annotation and enable CDI in containerd
function configure_container_runtime() {
	local cluster_name="${1}"
	local worker_id="${2}"

	worker="${cluster_name}-worker"
	if [ "${worker_id}" != "1" ]; then
		worker="${worker}${worker_id}"
	fi

	docker exec "${worker}" bash -c "\
	    nvidia-ctk config --set nvidia-container-runtime.modes.cdi.annotation-prefixes=nvidia.cdi.k8s.io/
	    nvidia-ctk runtime configure --runtime=containerd --set-as-default --cdi.enabled
	    systemctl restart containerd
	"
	
}

# Patch /proc/driver/nvidia 
function patch_proc_driver_nvidia() {
	local cluster_name="${1}"
	local num_gpus="${2}"
	local worker_id="${3}"
	local allowed_gpus="${4}"

	worker="${cluster_name}-worker"
	if [ "${worker_id}" != "1" ]; then
		worker="${worker}${worker_id}"
	fi

	# Unmount the masked /proc/driver/nvidia to allow
	# dynamically generated MIG devices to be discovered
	docker exec "${worker}" bash -c "
		umount -R /proc/driver/nvidia
	"
	
	# Make it so that calls into nvidia-smi / libnvidia-ml.so do not
	# attempt to recreate nvidia device nodes or reset their permissions if
	# tampered with
	docker exec "${worker}" bash -c "
		cp /proc/driver/nvidia/params root/gpu-params
		sed -i 's/^ModifyDeviceFiles: 1$/ModifyDeviceFiles: 0/' root/gpu-params
		mount --bind root/gpu-params /proc/driver/nvidia/params
	"
		
	# Remove the device nodes for all GPUs except those in the allowed list
	for gpu_id in $(seq 0 $(( ${num_gpus} - 1 ))); do
		for allowed in ${allowed_gpus}; do
			if [ "${gpu_id}" == "${allowed}" ]; then
				continue 2
			fi
		done
		docker exec "${worker}" bash -c "
			while umount /dev/nvidia${gpu_id}; do :; done || true
			rm -rf /dev/nvidia${gpu_id}
		"
	done
}

# Print GPUs for a given worker
function print_worker_gpus() {
	local cluster_name="${1}"
	local worker_id="${2}"
	
	worker="${cluster_name}-worker"
	if [ "${worker_id}" != "1" ]; then
		worker="${worker}${worker_id}"
	fi

	local gpu_info="$(
		docker exec ${worker} nvidia-smi --query-gpu=index,name,uuid --format=csv,noheader
	)"

	echo {} | jq -r --arg node ${worker} --arg gpu_info "${gpu_info}" '
	{
	  "node": $node,
	  "nvidia.com/gpu": (
	    ($gpu_info | split("\n"))
	    | map(split(", "))
	    | map( {
	        "index": .[0],
	        "name": .[1],
	        "uuid": .[2]
	      } )
	    | [ .[] ]
	  )
	}'
}

# Print GPUs for all workers
function print_all_worker_gpus() {
	local cluster_name="${1}"

	if [ "${cluster_name}" == "" ]; then
		cluster_name=$(kubectl config current-context)
		cluster_name=${cluster_name#kind-}
	fi

	local num_workers="$(
		kubectl --context=kind-${cluster_name} get nodes -o json | \
		jq -r '.items[] | select(.metadata.name | test("-worker[0-9]*$")) | .metadata.name' |
		wc -l
	)"

	for worker_id in $(seq ${num_workers}); do
		print_worker_gpus ${cluster_name} ${worker_id}
	done
}
