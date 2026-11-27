package main

import (
	"fmt"
	"os"

	"github.com/JBraunsmaJr/forge/internal/catrust"
)

// installCATrust makes this process trust the certificate authorities named
// by FORGE_CA_BUNDLE. It must run before any TLS connection is made, and it
// exits on a bundle that cannot be used: starting anyway would only move the
// failure to the first connection, with a less helpful message.
func installCATrust() {
	if _, err := catrust.Install(); err != nil {
		fmt.Fprintf(os.Stderr, "forge: cannot load CA certificates: %v\n", err)
		os.Exit(1)
	}
}
