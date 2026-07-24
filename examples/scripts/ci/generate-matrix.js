#!/usr/bin/env node

/**
 * Pure logic: transform platforms into Forge steps.
 * This is the testable part of your CI script.
 */
function generateMatrix(platforms) {
  return platforms.map(p => ({
    id: `build-${p.os}-${p.arch}`,
    image: p.image,
    env: {
      GOOS: p.os,
      GOARCH: p.arch
    },
    run: `go build -o dist/myapp-${p.os}-${p.arch} ./cmd/myapp`
  }));
}

// Thin entrypoint: only runs when executed directly.
if (require.main === module) {
  let inputData = {};
  
  // Read from stdin if available
  const fs = require('fs');
  try {
    const data = fs.readFileSync(0, 'utf8');
    if (data) {
      inputData = JSON.parse(data);
      console.error(`[info] Read generator context from stdin (pipeline: ${inputData.pipeline_name})`);
    }
  } catch (err) {
    // No stdin or not JSON, fallback to defaults
  }

  const platforms = [
    { os: "linux", arch: "amd64", image: "golang:1.26-alpine" },
    { os: "darwin", arch: "arm64", image: "golang:1.26-alpine" }
  ];

  const steps = generateMatrix(platforms);
  console.log(JSON.stringify(steps, null, 2));
}

module.exports = { generateMatrix };
