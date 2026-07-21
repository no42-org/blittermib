# ClusterFuzzLite build environment.
#
# base-builder-go ships the Go toolchain plus the OSS-Fuzz helpers
# (compile_native_go_fuzzer / go-118-fuzz-build) that convert a native
# `go test` fuzz target into a libFuzzer binary. The CFLite actions
# build this image, then run build.sh inside it.
FROM gcr.io/oss-fuzz-base/base-builder-go

COPY . $SRC/blittermib
WORKDIR $SRC/blittermib
COPY .clusterfuzzlite/build.sh $SRC/
