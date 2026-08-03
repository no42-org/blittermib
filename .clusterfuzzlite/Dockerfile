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
FROM gcr.io/oss-fuzz-base/base-builder-go@sha256:e5e5ecf4fb18a2f47bf0462c37e35728e8f6023ee4b7ff8bc5f903e6acba46da

COPY . $SRC/blittermib
WORKDIR $SRC/blittermib
COPY .clusterfuzzlite/build.sh $SRC/
