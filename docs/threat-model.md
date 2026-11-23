# nvkind Threat Model

This document gives the security architecture, the threat model, and the
security assumptions of `nvkind`. To report a vulnerability, see
[SECURITY.md](../SECURITY.md).

## Security Architecture & Context

`nvkind` is a command-line tool and a Go package (`pkg/nvkind`) that creates
[kind](https://kind.sigs.k8s.io/) clusters with access to NVIDIA GPUs. It is a
tool for development and test. It is not a network service: it has no
listener, no daemon, no database, and no authentication code of its own. It
runs with the privileges of the user who starts it, and it stops when the
command completes.

**Repository Exposure Classification:** Public.
Basis: the origin is a public repository on github.com. This document contains
public-safe detail only.

**Service Exposure Classification:** External / Regulated (high confidence).
Basis: open-source software that the public can install with `go install`.
There is no hosted service, there are no tagged releases, and the tool handles
no customer data.

### How it works

1. `nvkind cluster create` renders a kind cluster config from a Go template
   (`--config-template`) and a YAML values file (`--config-values`). A template
   can use the [sprig](https://masterminds.github.io/sprig/) functions and a
   `numGPUs` function that asks the driver through NVML.
2. It starts the cluster with the `kind` binary. It then stores the rendered
   config in the `nvkind-cluster-config` ConfigMap in the `default` namespace.
3. For each worker node that has GPUs, it runs scripts as root in the node
   container through `docker exec`. The scripts install the NVIDIA Container
   Toolkit with `apt`, configure containerd, mask
   `/proc/driver/nvidia/params`, and remove the `/dev/nvidia<N>` device nodes
   that the node must not see.
4. It registers the `nvidia` RuntimeClass with `kubectl`.

`nvkind cluster print-gpus` reads the ConfigMap again and runs `nvidia-smi` in
each GPU node.

### Trust boundaries

| Input or dependency | Trust |
|---|---|
| Command-line flags, environment variables, the config template and the values file | Trusted. They come from the operator who starts `nvkind`. |
| The `docker`, `kind` and `kubectl` binaries on `PATH`, and the kubeconfig | Trusted. `nvkind` passes arguments to these binaries directly, not through a shell. |
| The host NVIDIA driver, NVML and the NVIDIA Container Toolkit | Trusted platform. |
| The package sources used in a node: the `apt` sources of the node image and the NVIDIA Container Toolkit `apt` repository | Trusted. The repository key is downloaded over HTTPS when the node is set up. |
| The `nvkind-cluster-config` ConfigMap | Trusted. `nvkind` reads it back as a config template. |
| Workloads in the cluster | `nvkind` does not isolate them from the host. |

The scripts that `nvkind` runs in a node are fixed text. The only values that
it puts into them are integer GPU indices.

## Threat Model

These are the primary security concerns for this project, including the
example and CI code:

1. **Untrusted config template or values file:** A config template is code. It
   sets the full kind config, which includes host path mounts (`extraMounts`),
   port mappings and the node image. Template functions can also read the
   environment of the `nvkind` process, and `nvkind` stores the rendered config
   in the cluster. A template from an untrusted source can mount host paths
   into privileged node containers.
2. **Package supply chain during node setup:** `InstallContainerToolkit` in
   `pkg/nvkind/node.go` runs `apt-get` as root in each GPU node. It downloads
   the repository key and the repository list at run time, and it installs the
   newest `nvidia-container-toolkit` package without a version pin. A
   compromised package source gives code execution as root in a privileged
   node container.
3. **GPU isolation between nodes is not a security boundary:**
   `PatchProcDriverNvidia` and `removeDeviceNodes` hide GPUs from a node. They
   mask a driver parameter file and delete device nodes. kind node containers
   are privileged, so a root process in a node, or a privileged pod, can undo
   these steps. The code does not handle MIG devices. Do not use the GPU
   assignment of a node to separate tenants.
4. **Privileged operations on the host:** `nvkind` needs access to the Docker
   daemon, which is equal to root access on the host. The setup in
   [README.md](../README.md) also changes the Docker and NVIDIA Container
   Toolkit config for the full host: it makes `nvidia` the default runtime and
   enables `accept-nvidia-visible-devices-as-volume-mounts`. These changes
   apply to each container on the host, not only to kind nodes.
5. **Config stored in the cluster is read back as a template:** On later
   commands such as `print-gpus`, `nvkind` reads `nvkind-cluster-config` from
   the `default` namespace and parses it as a config template. A cluster user
   who can change that ConfigMap can change the config that `nvkind` uses on
   the host.
6. **Host preparation script:**
   `examples/gpu-operator/gpu-operator-kind-demo.sh` prepares a host with
   `sudo`. It installs Docker, kind, kubectl, Helm and the NVIDIA Container
   Toolkit. It downloads kind and kubectl without a version pin or a checksum
   check, and it pipes the Helm install script to a shell. Use it only on a
   development machine that you can rebuild.
7. **CI on GPU runners:** `.github/workflows/gpu-e2e.yaml` creates clusters on
   self-hosted GPU runners. Code from a fork runs there only after a maintainer
   vets the pull request and a bot copies it to a `pull-request/<N>` branch. An
   error in that vetting step gives untrusted code access to a GPU runner.

## Critical Security Assumptions

- **Single trust level on the host:** The host is a development or CI machine.
  Each user who can run `nvkind` or reach the Docker daemon is trusted as root.
- **Trusted workloads:** The cluster is not multi-tenant. `nvkind` adds no
  isolation between a node container and the host.
- **Correct GPU platform:** The host NVIDIA driver and the NVIDIA Container
  Toolkit are installed correctly and are up to date.
- **Genuine tools:** The `docker`, `kind` and `kubectl` binaries on `PATH` are
  genuine. `nvkind` does not verify them.
- **Protected kubeconfig:** Host file permissions protect the kubeconfig and
  the cluster credentials in it. `nvkind` stores no credentials.
- **Genuine package sources:** The package sources used during node setup are
  genuine, and the TLS connection to them is intact.
- **Trusted templates:** Config templates and values files come from an author
  that the operator trusts.

## Supported Versions

`nvkind` has no tagged releases. Security fixes land on the `main` branch.
Install or update from `main` to get them.

## Scope

Report a vulnerability in kind, Kubernetes, containerd or Docker to that
project. Report a vulnerability in the NVIDIA driver, the NVIDIA Container
Toolkit, the NVIDIA device plugin or the NVIDIA GPU Operator through the
channels in [SECURITY.md](../SECURITY.md). If the way that `nvkind` uses one of
these components makes a vulnerability possible, that is in scope for this
repository.
