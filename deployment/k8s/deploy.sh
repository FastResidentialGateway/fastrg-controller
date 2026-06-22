#!/usr/bin/env bash
#
# Deploy the FastRG Controller in HA mode (3 replicas, no hostPort) onto a real
# Kubernetes cluster, with etcd / PostgreSQL / Kafka running OUTSIDE the cluster.
#
# This is NOT the kind quickstart — for a local single-node demo use
# deployment/quickstart_k8s/ instead.
set -euo pipefail

SCRIPT_PATH="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
NAMESPACE="fastrg-system"
DATA_HOST=""        # override the external etcd/pg/kafka IP (default: as in the *-external.yml files)
LB_IP=""            # pin the LoadBalancer VIP (default: auto from the Cilium pool / provider)
APPLY_CILIUM_POOL="false"

usage() {
  cat <<EOF
Usage: $0 [options]
  -n, --namespace NS     Target namespace (default: fastrg-system)
      --data-host IP     External host for etcd/PostgreSQL/Kafka (rewrites *-external.yml)
      --lb-ip IP         Pin the LoadBalancer VIP (else "auto")
      --cilium-pool      Also apply cilium-lb-pool.yml (Cilium LB-IPAM only)
  -h, --help             Show this help

Backends are assumed external. Edit etcd-external.yml / postgresql-external.yml /
kafka-external.yml (or pass --data-host) and set the controller image in
controller.yml before deploying.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -n|--namespace) NAMESPACE="$2"; shift 2;;
    --data-host)    DATA_HOST="$2"; shift 2;;
    --lb-ip)        LB_IP="$2"; shift 2;;
    --cilium-pool)  APPLY_CILIUM_POOL="true"; shift;;
    -h|--help)      usage; exit 0;;
    *) echo "Unknown option: $1" >&2; usage; exit 1;;
  esac
done

render() {
  # Stream a manifest with namespace / data-host / lb-ip substitutions applied.
  local file="$1"
  sed \
    -e "s/namespace: fastrg-system/namespace: ${NAMESPACE}/g" \
    ${DATA_HOST:+-e "s/192\.168\.10\.215/${DATA_HOST}/g"} \
    ${LB_IP:+-e "s/lb-ipam-ips: \"auto\"/lb-ipam-ips: \"${LB_IP}\"/g"} \
    "${SCRIPT_PATH}/${file}"
}

echo ">> Namespace"
render namespace.yml | sed "s/name: fastrg-system/name: ${NAMESPACE}/g" | kubectl apply -f -

echo ">> RBAC (ServiceAccount + Role + RoleBinding)"
render rbac.yml | kubectl apply -f -

echo ">> External backend endpoints (etcd / PostgreSQL / Kafka)"
for f in etcd-external.yml postgresql-external.yml kafka-external.yml; do
  render "$f" | kubectl apply -f -
done

if [[ "$APPLY_CILIUM_POOL" == "true" ]]; then
  echo ">> Cilium LoadBalancer IP pool + L2 policy"
  kubectl apply -f "${SCRIPT_PATH}/cilium-lb-pool.yml"
fi

echo ">> Controller (Deployment + ClusterIP service)"
render controller.yml | kubectl apply -f -

echo ">> Controller LoadBalancer"
render controller-loadbalancer.yml | kubectl apply -f -

echo ">> PodDisruptionBudget"
render controller-pdb.yml | kubectl apply -f -

echo ">> Waiting for rollout (controller)"
kubectl rollout status deployment/fastrg-controller -n "${NAMESPACE}" --timeout=300s

echo ">> Done. Services:"
kubectl get svc -n "${NAMESPACE}" -l app=fastrg-controller
