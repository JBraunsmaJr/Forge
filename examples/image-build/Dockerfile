# A deliberately tiny image for trying out the docker_build step.
# The base image is fully qualified so the build also works under Podman,
# which does not assume docker.io for short names.
FROM docker.io/library/alpine:3.20

ARG VERSION=dev
RUN echo "${VERSION}" > /version

CMD ["cat", "/version"]
