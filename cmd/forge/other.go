package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func init() {
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "initialize a new forge pipeline",
		Run:   runInit,
	}
	rootCmd.AddCommand(initCmd)

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("forge %s\n", version)
		},
	}
	rootCmd.AddCommand(versionCmd)
}

func runInit(cmd *cobra.Command, args []string) {
	if _, err := os.Stat(".forge"); err == nil {
		if _, err := os.Stat(".forge/pipeline.yml"); err == nil {
			fmt.Println("✗ .forge/pipeline.yml already exists")
			return
		}
	} else {
		if err := os.MkdirAll(".forge", 0755); err != nil {
			fmt.Fprintf(os.Stderr, "✗ failed to create .forge directory: %v\n", err)
			os.Exit(1)
		}
	}

	kind, tmpl := detectProjectKind([]detector{
		{"go.mod", "Go", goTemplate},
		{"package.json", "Node.js", nodeTemplate},
		{"requirements.txt", "Python", pythonTemplate},
		{"Pipfile", "Python", pythonTemplate},
		{"pyproject.toml", "Python", pythonTemplate},
		{"Cargo.toml", "Rust", rustTemplate},
		{"Dockerfile", "Docker", dockerTemplate},
	})

	fmt.Printf("✨ Initializing %s project...\n", kind)

	if err := os.WriteFile(".forge/pipeline.yml", []byte(tmpl), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "✗ failed to write .forge/pipeline.yml: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✓ Created .forge/pipeline.yml")
	fmt.Println("\nNext steps:")
	fmt.Println("  1. Review the generated pipeline")
	fmt.Println("  2. Run it locally: forge run")
}

type detector struct {
	file string
	kind string
	tmpl string
}

func detectProjectKind(detectors []detector) (kind, tmpl string) {
	for _, d := range detectors {
		if _, err := os.Stat(d.file); err == nil {
			return d.kind, d.tmpl
		}
	}
	return "generic", genericTemplate
}

const genericTemplate = `name: generic-ci
on: [push]

steps:
  - id: hello
    image: alpine:latest
    run: echo "Hello from Forge!"
`

const goTemplate = `name: go-ci
on: [push]

steps:
  - id: test
    image: golang:1.26-alpine
    run: |
      go test ./... -v
`

const nodeTemplate = `name: node-ci
on: [push]

steps:
  - id: test
    image: node:20-alpine
    run: |
      npm install
      npm test
`

const pythonTemplate = `name: python-ci
on: [push]

steps:
  - id: test
    image: python:3.12-slim
    run: |
      pip install -r requirements.txt
      pytest
`

const rustTemplate = `name: rust-ci
on: [push]

steps:
  - id: test
    image: rust:1-slim
    run: |
      cargo test
`

const dockerTemplate = `name: docker-ci
on: [push]

steps:
  - id: build
    image: docker:latest
    run: |
      docker build -t app .
`
