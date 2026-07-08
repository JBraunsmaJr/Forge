# Dockerfile for Forge security policy transformers.
#
# This image contains all the example transformer scripts and the tools
# needed to run them. Build once, reference from multiple policies.
#
# Build:
#   docker build -t forge-security-policies:latest examples/policies/
#
# Use with Forge:
#   forge policy transformer container-security \
#     --image forge-security-policies:latest \
#     --command python3 /policies/container-security.py
#
#   forge policy transformer language-security \
#     --image forge-security-policies:latest \
#     --command python3 /policies/language-security.py

FROM python:3.11-alpine

# jq is useful for shell-based transformer scripts that prefer
# JSON manipulation over Python. Not required for the Python scripts.
RUN apk add --no-cache jq

# Copy transformer scripts into the image.
# In a real org you'd copy your own scripts here.
COPY container-security.py /policies/container-security.py
COPY language-security.py  /policies/language-security.py

# Default entrypoint: pass-through (returns pipeline unchanged).
# Overridden per-policy via --command.
ENTRYPOINT ["python3"]
CMD ["-c", "import json,sys; d=json.load(sys.stdin); print(json.dumps(d['steps']))"]