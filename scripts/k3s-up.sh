#!/usr/bin/env bash
# Start a single-node k3s on a CI runner, in --docker mode: it uses the
# runner's Docker as its container runtime, so images built or pulled with
# `docker` are runnable without a registry (the same model as Colima).
# Used by ci.yml and cd.yml. Context name: "default".
set -euo pipefail

: "${K3S_VERSION:?K3S_VERSION is required, e.g. v1.37.1+k3s1}"

curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION="$K3S_VERSION" \
  INSTALL_K3S_EXEC="--docker --disable traefik --disable metrics-server --write-kubeconfig-mode 644" sh -
mkdir -p ~/.kube && cp /etc/rancher/k3s/k3s.yaml ~/.kube/config

kubectl wait --for=condition=Ready node --all --timeout=180s
# The node can be Ready before k3s has created its add-ons; wait for CoreDNS
# to exist before waiting for it to roll out.
for _ in $(seq 60); do
  kubectl -n kube-system get deployment/coredns >/dev/null 2>&1 && break
  sleep 2
done
kubectl -n kube-system rollout status deployment/coredns --timeout=180s
