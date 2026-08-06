#!/usr/bin/env bash
# Lambda(provided.al2023 / arm64)用のバイナリを dist/<name>/bootstrap としてビルドします。
# CDKはこのディレクトリをそのままLambdaのコードアセットとして参照します。
set -euo pipefail

cd "$(dirname "$0")/.."

# CDKの lib/marina-stack.ts の lambdaFunctions と対応させること。
TARGETS=(lambda eventworker morningdigest reminderworker)

rm -rf dist
for target in "${TARGETS[@]}"; do
  outdir="dist/${target}"
  mkdir -p "${outdir}"
  echo "building ${target} -> ${outdir}/bootstrap"
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -tags lambda.norpc \
    -ldflags="-s -w" -o "${outdir}/bootstrap" "./cmd/${target}"
done

echo "done: $(ls -d dist/*)"
