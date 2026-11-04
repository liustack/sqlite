// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Command benchrun runs the driver benchmarks of bench/ and turns their
// results into the tables of docs/benchmarks.md and the README.
//
//	benchrun run [flags]          build, check and run every driver, in turns
//	benchrun publish -from DIR -name NAME
//	                              summarize a run into published/NAME.json
//	benchrun docs [-check]        write the tables from published/*.json
//
// Run it from bench/ (go run ./cmd/benchrun ...).
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(os.Args[2:])
	case "publish":
		err = cmdPublish(os.Args[2:])
	case "docs":
		err = cmdDocs(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "benchrun:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: benchrun run|publish|docs [flags]")
	os.Exit(2)
}
