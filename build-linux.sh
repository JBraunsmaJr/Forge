#!/bin/bash

/home/badger/sdk/go1.26.5/bin/go build -o forge ./cmd/forge/

cp forge /usr/local/bin/forge
