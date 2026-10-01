# ClusterFuzzLite build environment.
#
# base-builder-go ships the Go toolchain plus the OSS-Fuzz helpers
# (compile_native_go_fuzzer / go-118-fuzz-build) that convert a native
# `go test` fuzz target into a libFuzzer binary. The CFLite actions
# build this image, then run build.sh inside it.
#
# Pinned by digest so a rebuild of the upstream `latest` tag can't
# silently change the fuzzing toolchain (Scorecard Pinned-Dependencies).
# Dependabot's docker ecosystem watches this directory and opens the
# digest bumps.
FROM gcr.io/oss-fuzz-base/base-builder-go@sha256:95203d2b95d85f24f5e22ac3e144dc81aff49249f253dbe8eed40df70dbb1bbc

COPY . $SRC/blittermib
WORKDIR $SRC/blittermib
COPY .clusterfuzzlite/build.sh $SRC/
