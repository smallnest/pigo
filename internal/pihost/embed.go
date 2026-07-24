// Package pihost embeds the pi-extension host program (pihost.mjs) into the
// pigo binary. pihost.mjs is a self-contained Node ESM program that loads a pi
// extension using pi's real runtime and re-exposes it over pigo's JSON-RPC
// plugin protocol (see docs/superpowers/specs/2026-07-24-pi-extension-host-design.md).
//
// At install time, internal/pkgmgr.DistributeExtension writes these bytes next
// to a pi extension's payload (plugins/<name>.pkg/.pihost.mjs) and points a
// node-host launcher at them. Embedding keeps the host in lockstep with the
// pigo binary — there is no separate file to ship or version-skew.
package pihost

import _ "embed"

// Script is the embedded pihost.mjs source. Callers write it verbatim to disk.
//
//go:embed pihost.mjs
var Script []byte
