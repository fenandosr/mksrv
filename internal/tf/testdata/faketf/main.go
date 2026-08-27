// SPDX-License-Identifier: Apache-2.0

// Command faketf is a minimal stand-in for the terraform binary used by
// internal/tf unit tests. It answers `version` / `version -json` using
// FAKE_TF_VERSION (default 1.9.8) and ignores every other subcommand.
package main

import (
	"fmt"
	"os"
)

func main() {
	args := os.Args[1:]
	if len(args) >= 1 && args[0] == "version" {
		versionString := os.Getenv("FAKE_TF_VERSION")
		if versionString == "" {
			versionString = "1.9.8"
		}
		if hasFlag(args, "-json") {
			fmt.Printf(`{"terraform_version":%q,"platform":"fake_fake","provider_selections":{},"terraform_outdated":false}`+"\n", versionString)
			return
		}
		fmt.Printf("Terraform v%s\non fake_fake\n", versionString)
		return
	}
}

func hasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}
