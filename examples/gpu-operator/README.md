# GPU Operator Kind Demo

This example uses [gpu-operator-kind-demo.sh](./gpu-operator-kind-demo.sh) to create an `nvkind` cluster, install NVIDIA GPU Operator with `driver.enabled=false`, and verify GPU access with a sample workload.

## Prerequisites

The demo host must have:

- Linux on Ubuntu or Debian
- An NVIDIA GPU visible on the host
- A working host NVIDIA driver

Verify the host driver before running the demo:

```bash
nvidia-smi -L
```

If `nvidia-smi` is missing, install the host driver first:

```bash
sudo apt-get update
sudo apt-get install -y ubuntu-drivers-common
sudo ubuntu-drivers devices
sudo ubuntu-drivers autoinstall
sudo reboot
```

The script will install or configure these tools if they are missing:

- Go `1.24.3` by default, or `NVKIND_GO_VERSION`
- `nvkind` via `go install github.com/NVIDIA/nvkind/cmd/nvkind@latest`
- Docker
- `kind`
- `kubectl`
- `helm`
- `nvidia-container-toolkit`

## Run The Demo

From `examples/gpu-operator/`:

```bash
./gpu-operator-kind-demo.sh all
```

This will:

- check host prerequisites
- install `nvkind` if it is missing
- configure Docker for NVIDIA GPUs
- create the `nvkind-gpu-operator-demo` cluster
- install GPU Operator
- run the sample `gpu-pod` workload

## Script Modes

```bash
./gpu-operator-kind-demo.sh prepare
./gpu-operator-kind-demo.sh cluster
./gpu-operator-kind-demo.sh install
./gpu-operator-kind-demo.sh verify
./gpu-operator-kind-demo.sh status
./gpu-operator-kind-demo.sh cleanup
```

What each mode does:

- `prepare`: checks or installs host-side prerequisites and validates Docker GPU access
- `cluster`: prepares the host and creates the demo cluster
- `install`: installs GPU Operator into the current demo cluster
- `verify`: prints cluster state and runs the sample GPU workload
- `status`: checks whether the existing demo cluster is healthy end-to-end
- `cleanup`: deletes the demo cluster

## Status Check

To verify that an existing demo cluster is still healthy:

```bash
./gpu-operator-kind-demo.sh status
```

This checks:

- cluster existence
- node readiness
- NVIDIA runtime classes
- GPU Operator pod and daemonset health
- `ClusterPolicy` readiness
- end-to-end `gpu-pod` workload execution

## What Success Looks Like

A successful run ends after the `gpu-pod` workload completes, with output like:

```text
[demo] Demo completed successfully.
```

A successful status check ends with:

```text
[demo] Status: kind demo is healthy.
```

## Troubleshooting

If the script adds your user to the `docker` group and exits, refresh the shell group membership and rerun:

```bash
newgrp docker
./gpu-operator-kind-demo.sh all
```

If host driver validation fails, check:

```bash
nvidia-smi -L
docker run --rm --runtime=nvidia -e NVIDIA_VISIBLE_DEVICES=all ubuntu:22.04 nvidia-smi -L
```

If `nvidia-smi` is not found, install the host driver first:

```bash
sudo apt-get update
sudo apt-get install -y ubuntu-drivers-common
sudo ubuntu-drivers devices
sudo ubuntu-drivers autoinstall
sudo reboot
```

If cluster creation fails while joining the worker and kubelet logs show `inotify_init: too many open files`, raise the host file and inotify limits before retrying:

```bash
sudo sysctl -w fs.inotify.max_user_instances=1024
sudo sysctl -w fs.inotify.max_user_watches=524288
sudo sysctl -w fs.file-max=2097152
ulimit -n 1048576
```

For better persistence across reruns, also raise Docker's file descriptor limit and restart Docker:

```bash
sudo mkdir -p /etc/systemd/system/docker.service.d
sudo tee /etc/systemd/system/docker.service.d/limits.conf <<'EOF2'
[Service]
LimitNOFILE=1048576
EOF2
sudo systemctl daemon-reload
sudo systemctl restart docker
```

If the worker still fails to join, inspect the kind worker directly:

```bash
docker exec nvkind-gpu-operator-demo-worker systemctl status kubelet --no-pager -l
docker exec nvkind-gpu-operator-demo-worker journalctl -u kubelet --no-pager -n 200
docker exec nvkind-gpu-operator-demo-worker journalctl -u containerd --no-pager -n 200
```
