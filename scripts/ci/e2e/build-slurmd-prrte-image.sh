#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build the PRRTE slurmd image for the restart-in-allocation run and load it
# into the Kind workers that host the PRRTE nodesets.
#
# usage: build-slurmd-prrte-image.sh CLUSTER IMAGE SLURMD_IMAGE
set -euo pipefail

if (( $# != 3 )); then
  echo "usage: $0 CLUSTER IMAGE SLURMD_IMAGE" >&2
  exit 2
fi
cluster="$1"
image="$2"
slurmd_image="$3"
context="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/images/slurmd-prrte"

docker build --build-arg "SLURMD_IMAGE=$slurmd_image" --tag "$image" "$context"

nodes="$(kubectl get nodes \
  --selector 'heimdall.nvidia.com/slurm-node in (prrte-a-0,prrte-b-0,prrte-c-0)' \
  --output 'jsonpath={.items[*].metadata.name}')"
if [[ -z "$nodes" ]]; then
  echo "error: no Kind worker is labeled for a PRRTE node; create the cluster with kind-node-separated-prrte.yaml" >&2
  exit 1
fi
kind load docker-image --name "$cluster" --nodes "${nodes// /,}" "$image"
