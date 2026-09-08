# Registry fork core CI

Railway builds this image on the fork's main branch with no credentials.
All Go tests and the four validation tool builds must pass during the image
build, so a failed check fails the deployment. The runtime prints the exact
source and deployment identity for the successfully built image and exits.

This is the portable core of the upstream validation workflow. Changed-server
image build, isolated MCP enumeration, and catalog generation still require a
Docker executor. The GitHub PR validation workflow stays enabled until that
executor and PR-source coverage are proven. A core pass does not attest a server
image or prove acceptance into Docker's registry.

The inherited pin automation and private security-review dispatchers require
Docker-owned GitHub App credentials that are not present in this contribution
fork. Their disabled state is retirement of inapplicable upstream automation,
not a completed migration of Docker's services.
