#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# A job that declares PRRTE in-place recovery: the controller wakes its batch
# script with SIGUSR2 when it orders a restart in the allocation, and the
# script carries the order out. Waiting through `wait` lets bash run the trap
# as soon as the signal arrives instead of after a long foreground command.

trap 'bash /jobs/restart-in-allocation.sh &' USR2
end=$((SECONDS + 300))
while (( SECONDS < end )); do
  sleep 1 &
  wait $!
done
