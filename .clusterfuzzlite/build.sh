#!/bin/bash -eu
# Copyright 2026 Ronny Trommer <ronny@no42.org>
# SPDX-License-Identifier: MIT
#
# Build native Go fuzz targets into libFuzzer binaries for
# ClusterFuzzLite. compile_native_go_fuzzer imports the package under
# test, so only importable packages qualify — cmd/mib-ingest's
# FuzzNormalizeLastUpdated lives in `package main` and stays on native
# `go test -fuzz` (make fuzz) until its function is moved to an
# importable package.

cd "$SRC/blittermib"

# go-118-fuzz-build rewrites each `func Fuzz*(*testing.F)` to use its
# libFuzzer shim, so its `testing` package must resolve. Pull it into
# the module graph at build time (it never enters the committed go.mod —
# only the real code's deps do). Network is available in the OSS-Fuzz
# build; this is the documented native-Go-fuzzer step.
go get github.com/AdamKorcz/go-118-fuzz-build/testing

compile_native_go_fuzzer \
  github.com/no42-org/blittermib/internal/walk \
  FuzzParse \
  fuzz_parse
