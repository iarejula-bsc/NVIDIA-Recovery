# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Slinky slurmd with a PRRTE runtime that can restart a job inside its
# allocation, for the opt-in restart-in-allocation E2E run
# (E2E_RESTART_IN_ALLOCATION).
#
# Restart in allocation needs PRRTE fixes that are in master but not in a
# release yet, so the image builds PMIx and PRRTE master at pinned commits.
ARG SLURMD_IMAGE=ghcr.io/slinkyproject/slurmd:25.05-ubuntu24.04

FROM ubuntu:24.04 AS build
RUN apt-get update && apt-get install -y --no-install-recommends \
        autoconf automake build-essential ca-certificates flex git libevent-dev \
        libhwloc-dev libjansson-dev libtool perl pkg-config python3 zlib1g-dev \
    && rm -rf /var/lib/apt/lists/*
ENV PREFIX=/opt/prrte
ARG PMIX_REPO=https://github.com/openpmix/openpmix.git
ARG PMIX_COMMIT=6a663a5e827fcec7b0da94607b4d42428b7815b3
RUN git init /src/openpmix && cd /src/openpmix \
    && git remote add origin "$PMIX_REPO" \
    && git fetch --depth 1 origin "$PMIX_COMMIT" && git checkout FETCH_HEAD \
    && git submodule update --init --recursive --depth 1 \
    && ./autogen.pl && ./configure --prefix="$PREFIX" --disable-debug \
    && make -j"$(nproc)" install
ARG PRRTE_REPO=https://github.com/openpmix/prrte.git
ARG PRRTE_COMMIT=8f93594611e685b21e2918bf23ae22b139178e3a
RUN git init /src/prrte && cd /src/prrte \
    && git remote add origin "$PRRTE_REPO" \
    && git fetch --depth 1 origin "$PRRTE_COMMIT" && git checkout FETCH_HEAD \
    && git submodule update --init --recursive --depth 1 \
    && ./autogen.pl && ./configure --prefix="$PREFIX" --disable-debug --with-jansson \
                                   --enable-slurm-extensions --with-pmix="$PREFIX" \
    && make -j"$(nproc)" install
COPY prrte_restart.c /src/
RUN gcc -Wall -O2 -o "$PREFIX/bin/prrte_restart" /src/prrte_restart.c \
        -I"$PREFIX/include" -L"$PREFIX/lib" -lpmix -Wl,-rpath,"$PREFIX/lib"

FROM ${SLURMD_IMAGE}
RUN apt-get update && apt-get install -y --no-install-recommends \
        libevent-2.1-7t64 libevent-core-2.1-7t64 libevent-pthreads-2.1-7t64 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /opt/prrte /opt/prrte
RUN printf '/opt/prrte/lib\n' > /etc/ld.so.conf.d/prrte.conf && ldconfig
