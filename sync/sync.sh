#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

dir=$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd ) ; readonly dir
cd "${dir}/.."

# Stage 1 create chart in the ./dist folder
set -x
kubebuilder edit --plugins=helm/v1-alpha
{ set +x; } 2>/dev/null

# Stage 1 sync - intermediate to the ./vendir folder
set -x
vendir sync
helm dependency update helm/image-distribution-operator
{ set +x; } 2>/dev/null

# Patches
./sync/patches/helpers/patch.sh
./sync/patches/manager/patch.sh
