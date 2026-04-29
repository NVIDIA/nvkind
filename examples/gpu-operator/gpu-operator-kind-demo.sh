#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
CLUSTER_NAME="${NVKIND_DEMO_CLUSTER_NAME:-nvkind-gpu-operator-demo}"
KUBECONFIG_CONTEXT="kind-${CLUSTER_NAME}"
HELM_RELEASE_NAME="${NVKIND_GPU_OPERATOR_RELEASE:-nvidia-gpu-operator}"
HELM_TIMEOUT="${NVKIND_GPU_OPERATOR_TIMEOUT:-20m}"
MODE="${1:-all}"
GO_VERSION="${NVKIND_GO_VERSION:-1.24.3}"

if [[ "$(id -u)" -eq 0 ]]; then
  SUDO=""
else
  SUDO="sudo"
fi

log() {
  printf '[demo] %s\n' "$*"
}

warn() {
  printf '[demo][warn] %s\n' "$*" >&2
}

fail() {
  printf '[demo][error] %s\n' "$*" >&2
  exit 1
}

require_linux() {
  [[ "$(uname -s)" == "Linux" ]] || fail 'This demo script currently supports Linux only.'
}

require_command() {
  local name="$1"
  command -v "${name}" >/dev/null 2>&1 || fail "'${name}' is required for this action. Install it first and rerun this script."
}

load_os_release() {
  [[ -f /etc/os-release ]] || fail 'Missing /etc/os-release; unsupported host OS.'
  # shellcheck disable=SC1091
  source /etc/os-release
  OS_ID="${ID:-}"
}

require_apt_host() {
  load_os_release
  case "${OS_ID}" in
    ubuntu|debian) ;;
    *) fail "This demo script currently supports Ubuntu/Debian only (detected: ${OS_ID:-unknown})." ;;
  esac
  command -v apt-get >/dev/null 2>&1 || fail 'apt-get is required on this host.'
}

apt_install() {
  local packages=("$@")
  if [[ "${#packages[@]}" -eq 0 ]]; then
    return 0
  fi
  log "Installing packages: ${packages[*]}"
  ${SUDO} apt-get update
  ${SUDO} apt-get install -y "${packages[@]}"
}

ensure_base_packages() {
  require_apt_host
  apt_install ca-certificates curl gnupg lsb-release pciutils tar
}

version_ge() {
  [[ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -n1)" == "$2" ]]
}

ensure_go() {
  local current_go=""
  if command -v go >/dev/null 2>&1; then
    current_go="$(go env GOVERSION 2>/dev/null || true)"
    current_go="${current_go#go}"
  fi

  if [[ -n "${current_go}" ]] && version_ge "${current_go}" "${GO_VERSION}"; then
    return 0
  fi

  log "Installing Go ${GO_VERSION}"
  ensure_base_packages
  curl -fsSL -o /tmp/go.tgz "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"
  ${SUDO} rm -rf /usr/local/go
  ${SUDO} tar -C /usr/local -xzf /tmp/go.tgz
  rm -f /tmp/go.tgz
  export PATH="/usr/local/go/bin:${PATH}"
  command -v go >/dev/null 2>&1 || fail 'Go installation completed but the go binary is still not on PATH.'
}

ensure_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    log 'Installing Docker Engine'
    ensure_base_packages
    ${SUDO} install -m 0755 -d /etc/apt/keyrings
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg | ${SUDO} gpg --dearmor -o /etc/apt/keyrings/docker.gpg
    ${SUDO} chmod a+r /etc/apt/keyrings/docker.gpg
    local arch codename
    arch="$(dpkg --print-architecture)"
    codename="$(${SUDO} bash -lc '. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}"')"
    ${SUDO} bash -lc "cat > /etc/apt/sources.list.d/docker.list <<'LIST'
deb [arch=${arch} signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu ${codename} stable
LIST"
    apt_install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  fi

  ${SUDO} systemctl enable --now docker

  if ! docker version >/dev/null 2>&1; then
    if ! id -nG "${USER}" | tr ' ' '\n' | grep -qx docker; then
      ${SUDO} usermod -aG docker "${USER}"
      fail "Added ${USER} to the docker group. Start a new shell (or run 'newgrp docker') and rerun this script."
    fi
    fail 'Docker is installed, but the current shell cannot access /var/run/docker.sock.'
  fi
}

ensure_kind() {
  if command -v kind >/dev/null 2>&1; then
    return 0
  fi
  log 'Installing kind'
  curl -Lo /tmp/kind https://kind.sigs.k8s.io/dl/latest/kind-linux-amd64
  chmod +x /tmp/kind
  ${SUDO} mv /tmp/kind /usr/local/bin/kind
}

ensure_kubectl() {
  if command -v kubectl >/dev/null 2>&1; then
    return 0
  fi
  log 'Installing kubectl'
  local version
  version="$(curl -Ls https://dl.k8s.io/release/stable.txt)"
  curl -Lo /tmp/kubectl "https://dl.k8s.io/release/${version}/bin/linux/amd64/kubectl"
  chmod +x /tmp/kubectl
  ${SUDO} mv /tmp/kubectl /usr/local/bin/kubectl
}

ensure_helm() {
  if command -v helm >/dev/null 2>&1; then
    return 0
  fi
  log 'Installing Helm'
  curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
}

ensure_nvkind() {
  if command -v nvkind >/dev/null 2>&1 && nvkind --help >/dev/null 2>&1; then
    return 0
  fi

  ensure_go
  export PATH="$(go env GOPATH 2>/dev/null || printf '%s' "${HOME}/go")/bin:/usr/local/go/bin:${PATH}"

  log 'Installing nvkind'
  go install github.com/NVIDIA/nvkind/cmd/nvkind@latest
  command -v nvkind >/dev/null 2>&1 || fail "'nvkind' was installed but is not on PATH. Add \"\$(go env GOPATH)/bin\" to PATH and rerun this script."
  nvkind --help >/dev/null 2>&1 || fail "'nvkind' was installed but is not runnable. Fix the installation before rerunning this script."
}

ensure_gpu_present() {
  if ! lspci | grep -qi nvidia; then
    fail 'No NVIDIA GPU detected via lspci on this host.'
  fi
}

ensure_host_driver() {
  ensure_gpu_present
  if ! command -v nvidia-smi >/dev/null 2>&1; then
    fail "'nvidia-smi' is not available on the host. Install the NVIDIA host driver and reboot before rerunning this script. On Ubuntu/Debian you can usually use: sudo apt-get update && sudo apt-get install -y ubuntu-drivers-common && sudo ubuntu-drivers devices && sudo ubuntu-drivers autoinstall && sudo reboot"
  fi
  if ! nvidia-smi -L >/dev/null 2>&1; then
    warn 'Host NVIDIA driver check failed:'
    nvidia-smi -L || true
    fail "Host NVIDIA driver is not healthy. Fix 'nvidia-smi -L' before rerunning this script. If the driver is missing, on Ubuntu/Debian you can usually use: sudo apt-get update && sudo apt-get install -y ubuntu-drivers-common && sudo ubuntu-drivers devices && sudo ubuntu-drivers autoinstall && sudo reboot"
  fi
}

ensure_nvidia_container_toolkit() {
  if ! command -v nvidia-ctk >/dev/null 2>&1; then
    log 'Installing NVIDIA Container Toolkit'
    ensure_base_packages
    curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | ${SUDO} gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
    curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list \
      | sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' \
      | ${SUDO} tee /etc/apt/sources.list.d/nvidia-container-toolkit.list >/dev/null
    apt_install nvidia-container-toolkit
  fi

  log 'Configuring Docker for NVIDIA GPUs'
  ${SUDO} nvidia-ctk runtime configure --runtime=docker --set-as-default --cdi.enabled
  ${SUDO} nvidia-ctk config --set accept-nvidia-visible-devices-as-volume-mounts=true --in-place
  ${SUDO} systemctl restart docker

  log 'Verifying Docker GPU access'
  docker run --rm --runtime=nvidia -e NVIDIA_VISIBLE_DEVICES=all ubuntu:22.04 nvidia-smi -L >/dev/null
  docker run --rm -v /dev/null:/var/run/nvidia-container-devices/all ubuntu:22.04 nvidia-smi -L >/dev/null
}

cluster_exists() {
  require_command kind
  kind get clusters | grep -qx "${CLUSTER_NAME}"
}

use_demo_context() {
  require_command kubectl
  cluster_exists || fail "Kind cluster '${CLUSTER_NAME}' does not exist. Run '$0 all' or '$0 cluster' first."
  kubectl config use-context "${KUBECONFIG_CONTEXT}" >/dev/null 2>&1 || fail "kubectl context '${KUBECONFIG_CONTEXT}' is not available."
}

cleanup_cluster() {
  if cluster_exists; then
    log "Deleting existing cluster ${CLUSTER_NAME}"
    kind delete cluster --name "${CLUSTER_NAME}"
  fi
}

create_cluster() {
  cleanup_cluster
  log "Creating cluster ${CLUSTER_NAME}"
  nvkind cluster create --name "${CLUSTER_NAME}" --retain
  kubectl config use-context "${KUBECONFIG_CONTEXT}" >/dev/null
}

install_gpu_operator() {
  kubectl config use-context "${KUBECONFIG_CONTEXT}" >/dev/null
  log 'Installing GPU Operator'
  helm repo add nvidia https://helm.ngc.nvidia.com/nvidia >/dev/null 2>&1 || true
  helm repo update
  kubectl create namespace gpu-operator --dry-run=client -o yaml | kubectl apply -f -
  kubectl label namespace gpu-operator pod-security.kubernetes.io/enforce=privileged --overwrite
  helm upgrade -i "${HELM_RELEASE_NAME}" nvidia/gpu-operator \
    --namespace gpu-operator \
    --wait \
    --timeout "${HELM_TIMEOUT}" \
    --set cdi.enabled=true \
    --set driver.enabled=false \
    --set operator.runtimeClass=nvidia
}

wait_for_pod_terminal() {
  local namespace="$1"
  local name="$2"
  local timeout_seconds="$3"
  local phase
  local start
  start="$(date +%s)"
  while true; do
    phase="$(kubectl get pod -n "${namespace}" "${name}" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    case "${phase}" in
      Running|Succeeded)
        return 0
        ;;
      Failed)
        kubectl describe pod -n "${namespace}" "${name}" || true
        kubectl logs -n "${namespace}" "${name}" || true
        fail "Pod ${namespace}/${name} failed."
        ;;
    esac
    if (( $(date +%s) - start > timeout_seconds )); then
      kubectl describe pod -n "${namespace}" "${name}" || true
      fail "Timed out waiting for pod ${namespace}/${name} to become Running or Succeeded."
    fi
    sleep 5
  done
}

print_cluster_summary() {
  log 'Cluster summary'
  kubectl get nodes
  kubectl get runtimeclass
  kubectl get clusterpolicy -A || true
  kubectl get pods -n gpu-operator
  kubectl get ds -n gpu-operator
}

verify_install() {
  kubectl config use-context "${KUBECONFIG_CONTEXT}" >/dev/null
  print_cluster_summary

  log 'Running sample GPU workload'
  kubectl delete pod gpu-pod --ignore-not-found >/dev/null 2>&1 || true
  kubectl apply -f "${SCRIPT_DIR}/gpu-pod.yml"
  wait_for_pod_terminal default gpu-pod 600
  kubectl logs gpu-pod
}

check_nodes_ready() {
  local not_ready
  not_ready="$(kubectl get nodes --no-headers | awk '$2 != "Ready" {print $1}')"
  [[ -z "${not_ready}" ]] || fail "Not all cluster nodes are Ready: $(echo "${not_ready}" | paste -sd ', ' -)"
}

check_runtimeclasses_ready() {
  kubectl get runtimeclass nvidia >/dev/null 2>&1 || fail "RuntimeClass 'nvidia' was not found in '${KUBECONFIG_CONTEXT}'."
}

check_clusterpolicy_ready() {
  local state
  state="$(kubectl get clusterpolicy cluster-policy -o jsonpath='{.status.state}' 2>/dev/null || true)"
  [[ -n "${state}" ]] || fail 'ClusterPolicy cluster-policy was not found.'
  [[ "${state,,}" == 'ready' ]] || fail "ClusterPolicy cluster-policy is not ready (state: ${state})."
}

check_gpu_operator_pods_ready() {
  local bad
  bad="$(kubectl get pods -n gpu-operator --no-headers | awk '
    {
      split($2, ready, "/")
      if ($3 == "Running" && ready[1] != ready[2]) {
        print $1 " " $2 " " $3
      } else if ($3 != "Running" && $3 != "Completed") {
        print $1 " " $2 " " $3
      }
    }
  ')"
  [[ -z "${bad}" ]] || fail "Some gpu-operator pods are not healthy:\n${bad}"
}

run_status_workload() {
  log 'Running status GPU workload'
  kubectl delete pod gpu-pod-status --ignore-not-found >/dev/null 2>&1 || true
  sed 's/\bname: gpu-pod\b/name: gpu-pod-status/g' "${SCRIPT_DIR}/gpu-pod.yml" | kubectl apply -f -
  wait_for_pod_terminal default gpu-pod-status 600
  kubectl logs gpu-pod-status
  kubectl delete pod gpu-pod-status --ignore-not-found >/dev/null 2>&1 || true
}

status_install() {
  use_demo_context
  print_cluster_summary
  check_nodes_ready
  check_runtimeclasses_ready
  check_clusterpolicy_ready
  check_gpu_operator_pods_ready
  run_status_workload
  log 'Status: kind demo is healthy.'
}

dump_diagnostics() {
  warn 'Collecting diagnostics'
  kubectl config current-context 2>/dev/null || true
  kubectl get nodes 2>/dev/null || true
  kubectl get pods -A 2>/dev/null || true
  kubectl get ds -n gpu-operator 2>/dev/null || true
  kubectl get clusterpolicy -A 2>/dev/null || true
}

on_error() {
  dump_diagnostics || true
}
trap on_error ERR

prepare_host() {
  require_linux
  ensure_base_packages
  ensure_docker
  ensure_kind
  ensure_kubectl
  ensure_helm
  ensure_nvkind
  ensure_host_driver
  ensure_nvidia_container_toolkit
}

run_all() {
  prepare_host
  create_cluster
  install_gpu_operator
  verify_install
  log 'Demo completed successfully.'
}

case "${MODE}" in
  all)
    run_all
    ;;
  prepare)
    prepare_host
    ;;
  cluster)
    prepare_host
    create_cluster
    ;;
  install)
    install_gpu_operator
    ;;
  verify)
    verify_install
    ;;
  status)
    status_install
    ;;
  cleanup)
    cleanup_cluster
    ;;
  *)
    cat >&2 <<USAGE
Usage: $0 [all|prepare|cluster|install|verify|status|cleanup]

  all      Check/install prerequisites, install nvkind if needed, create cluster, install GPU Operator, run sample pod
  prepare  Check/install prerequisites, configure Docker + NVIDIA toolkit, install nvkind if needed
  cluster  Prepare host and create the nvkind cluster only
  install  Install GPU Operator into the current '${KUBECONFIG_CONTEXT}' context
  verify   Verify the current '${KUBECONFIG_CONTEXT}' context and run the sample pod
  status   Check whether the existing '${KUBECONFIG_CONTEXT}' demo cluster is healthy end-to-end
  cleanup  Delete the demo cluster if it exists
USAGE
    exit 1
    ;;
esac
