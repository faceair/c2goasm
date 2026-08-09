#!/usr/bin/env bash
# Regenerate the converted Go assembly (simd_arm64.s / simd_amd64.s) from
# c/simd.c. Requires clang (arm64) or gcc (amd64) and c2goasm on PATH
# (`go install github.com/faceair/c2goasm/cmd/c2goasm@latest`).
set -euo pipefail

cd "$(dirname "$0")"

TARGET="${C2GOASM_TARGET:-$(go env GOARCH)}"
case "$TARGET" in
arm64 | aarch64)
	clang -S -O3 \
		-fomit-frame-pointer -fno-stack-protector \
		-fno-asynchronous-unwind-tables -fno-unwind-tables \
		-mno-outline -ffixed-x27 -ffixed-x28 \
		--target=arm64-apple-darwin \
		-o c/simd.s c/simd.c
	c2goasm -t arm64 c/simd.s simd_arm64.s
	;;
amd64 | x86_64)
	gcc -S -O3 -mavx2 -masm=intel \
		-fomit-frame-pointer -fno-stack-protector -mno-red-zone \
		-fno-asynchronous-unwind-tables -fno-unwind-tables \
		-ffixed-rbp -ffixed-r14 -ffixed-r11 \
		-o c/simd.s c/simd.c
	c2goasm -t amd64 c/simd.s simd_amd64.s
	;;
*)
	echo "unsupported C2GOASM_TARGET: $TARGET (use arm64 or amd64)" >&2
	exit 1
	;;
esac
