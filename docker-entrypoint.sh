#!/bin/sh
# Copyright 2026 Ronny Trommer <ronny@no42.org>
# SPDX-License-Identifier: MIT
#
# Container entrypoint: repair ownership of the state directories,
# then drop privileges to the unprivileged `blittermib` user.
#
# Why this exists: the reference compose bind-mounts a host intake dir
# INTO the data volume's corpus tree (./import -> <data>/mibs/import).
# On first boot the data volume is empty, so Docker creates the
# intermediate <data>/mibs mountpoint as ROOT before the process
# starts. A non-root process (uid 1000) then cannot create
# mibs/ietf, mibs/iana, … and every import fails with
# "permission denied: mkdir <data>/mibs/<dir>". Chowning here — the
# one moment we still hold root — fixes it with no manual step.
set -e

DATA_DIR=/var/lib/blittermib/data   # matches the image's default CMD (-data)
MIBS_DIR="${DATA_DIR}/mibs"
IMPORT_DIR="${MIBS_DIR}/import"

# Only a privileged (root) start can repair ownership. When the image
# is run with a --user override we are already unprivileged: skip the
# chown (it would only fail) and let the binary's read-only fallbacks
# handle any genuinely unwritable path.
if [ "$(id -u)" = "0" ]; then
    # Non-recursive on the volume dirs: only the Docker-created
    # mountpoint parent is mis-owned; the corpus beneath is written by
    # the process and must keep its own ownership. Recursive on import
    # — it is the host bind dir and may hold root-owned pending drops.
    # || true keeps a genuinely unrepairable path (root-squashed NFS,
    # unmapped userns) from aborting boot — the binary's read-only
    # fallbacks take over. stderr is NOT silenced: a real chown failure
    # must show in the container log instead of surfacing later as an
    # opaque EACCES. The [ -d ] guards suppress the only expected
    # failures (paths absent until first import).
    chown blittermib:blittermib "${DATA_DIR}" || true
    [ -d "${MIBS_DIR}" ] && chown blittermib:blittermib "${MIBS_DIR}" || true
    [ -d "${IMPORT_DIR}" ] && chown -R blittermib:blittermib "${IMPORT_DIR}" || true
    exec su-exec blittermib:blittermib /usr/local/bin/blittermib "$@"
fi

exec /usr/local/bin/blittermib "$@"
