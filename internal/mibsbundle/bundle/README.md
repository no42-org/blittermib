# bundle/

This directory contains the IETF/IANA standard MIB files that ship
embedded inside the blittermib binary.

It is intentionally empty in source control — populate it via:

```
make fetch-standard-mibs
```

which downloads libsmi's standard MIB collection (SNMPv2-SMI,
SNMPv2-TC, IF-MIB, IP-MIB, TCP-MIB, UDP-MIB, …) and copies the
relevant files here. The next `go build` embeds them.

This README is preserved by `mibsbundle.Stage` (it is not a MIB
and gets skipped during extraction). Its presence ensures
`go:embed bundle` always has something to embed even when the
bundle has not been populated yet.
