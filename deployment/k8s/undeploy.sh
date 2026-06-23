#!/usr/bin/env bash
#
# Remove the FastRG Controller HA deployment. External etcd/PostgreSQL/Kafka are
# left untouched (they live outside the cluster). The namespace is kept by
# default; pass --delete-namespace to remove it.
set -euo pipefail

SCRIPT_PATH="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
NAMESPACE="fastrg-system"
DELETE_NS="false"

while [[ $# -gt 0 ]]; do
  case "$1" in
    -n|--namespace) NAMESPACE="$2"; shift 2;;
    --delete-namespace) DELETE_NS="true"; shift;;
    -h|--help) echo "Usage: $0 [-n NS] [--delete-namespace]"; exit 0;;
    *) echo "Unknown option: $1" >&2; exit 1;;
  esac
done

for f in controller-pdb.yml controller-loadbalancer.yml controller.yml \
         kafka-external.yml postgresql-external.yml etcd-external.yml secret.yml rbac.yml; do
  sed "s/namespace: fastrg-system/namespace: ${NAMESPACE}/g" "${SCRIPT_PATH}/${f}" \
    | kubectl delete --ignore-not-found -f - || true
done

# Cilium pool is cluster-scoped; only present if it was applied.
kubectl delete --ignore-not-found -f "${SCRIPT_PATH}/cilium-lb-pool.yml" 2>/dev/null || true

if [[ "$DELETE_NS" == "true" ]]; then
  kubectl delete namespace "${NAMESPACE}" --ignore-not-found
fi

echo ">> Removed controller resources from namespace ${NAMESPACE}."
