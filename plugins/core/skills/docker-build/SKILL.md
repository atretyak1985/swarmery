---
name: docker-build
description: "Use this skill when building or pushing Docker images for the project's services (the main app, the device service) to the cloud container registry. Don't use it for deploys (use deployment) or Dockerfile template editing without a build (use code-quality)."
version: "1.0.0"
owner: "agentry-core"
---

# Purpose

Build and push Docker images for the project's services to the cloud container registry (see `.claude/project.json` → `cloud.provider`). Covers multi-arch builds (e.g., ARM64 for a device/edge service on a Raspberry Pi, AMD64 for the main web app), tagging conventions, and registry authentication. This skill handles image creation only; deploying the image is handled by `deployment`.

Placeholders: `<mainApp>` = `project.json → mainApp`; `<device>` = `project.json → device` (the device/edge service, if the project has one).

# When to use this skill (triggers)

- Building a Docker image for the main app or the device service
- Pushing a built image to the cloud container registry
- Setting up Docker buildx for multi-architecture builds
- Debugging image build failures (build context, layer caching, platform mismatch)

# When NOT to use this skill (anti-triggers)

- Deploying an image to the runtime environment -- use `deployment`
- Editing deployment config templates or values files -- use `deployment`
- Editing a Dockerfile without running a build -- use `code-quality` for review
- Pushing to the production registry from a local dev machine without CI -- stop and escalate
- Tests have not passed for the code being built -- build gate not met, do not proceed

# Required environment (Runtime: .claude/skills/docker-build/SKILL.md)

- Tools/libraries: `docker` (with buildx plugin), the cloud provider's CLI (for registry auth)
- Environment variable: `IMAGE_REGISTRY` -- the container registry URL (e.g., `<region>-docker.pkg.dev/<project-id>/<repository>`; region from `project.json → cloud.region`). Do not hardcode the cloud project ID.

# Inputs

- `service: string` -- which service to build: the main app (`<mainApp>`) or the device service (`<device>`)
- `tag: string` -- image tag (git short hash for dev, semver for release)
- `push: boolean` -- whether to push to the registry after building (default: false)

# Outputs

**Format:** Build result inlined in agent response.

**Length budget:** Build result max 10 lines. If build fails, include last 20 lines of build output.

**Output template:**

```
## Build Result
Build: {service}
Image: $IMAGE_REGISTRY/{image-name}:{tag}
Platform: {architecture}
Pushed: {yes|no}
Digest: {sha256:... if pushed, N/A if local only}

### Confidence: {HIGH|MEDIUM|LOW} -- {rationale}
```

# Procedure (Checkpoint: after each step)

1. **Authenticate with the cloud registry** -- Ensure Docker is configured for the registry.
   ```bash
   # Local dev auth only -- CI pipelines use WIF (see gcp-cicd-auth skill)
   gcloud auth configure-docker <region>-docker.pkg.dev
   ```
   Checkpoint: `docker login` succeeds for the registry host.

2. **Set up buildx** -- Ensure a buildx builder exists for the target platform.
   ```bash
   docker buildx create --name multiarch-builder --use 2>/dev/null || docker buildx use multiarch-builder
   docker buildx inspect --bootstrap
   ```
   Checkpoint: Builder is active and supports the target platform.

3. **Determine build parameters** -- Based on the service:

   | Service | Image name | Platform | Dockerfile | Build context |
   |---------|-----------|----------|------------|---------------|
   | `<device>` (edge, on RPi) | `<device-image>` | `linux/arm64` | `Dockerfile.optimized` | `<device>/` |
   | `<mainApp>` (web) | `<mainApp>` | `linux/amd64` | `docker/Dockerfile` | `apps/<mainApp>/` |

4. **Build the image** -- Run the build command. Do NOT pass `NEXT_PUBLIC_*` variables as `--build-arg`.

   **Device service**:
   ```bash
   cd <device>
   COMMIT_HASH=$(git rev-parse --short HEAD)

   docker buildx build \
     --platform linux/arm64 \
     -t $IMAGE_REGISTRY/<device-image>:$COMMIT_HASH \
     -f Dockerfile.optimized .
   ```

   **Main app**:
   ```bash
   cd apps/<mainApp>
   COMMIT_HASH=$(git rev-parse --short HEAD)

   # No --build-arg for NEXT_PUBLIC_* variables.
   # Client config is injected at runtime via window.__ENV__ bridge.
   docker buildx build \
     --platform linux/amd64 \
     -t $IMAGE_REGISTRY/<mainApp>:$COMMIT_HASH \
     -f docker/Dockerfile .
   ```

   The main-app image must be environment-agnostic per 12-factor. Client-visible configuration (like a maps API key) is injected at runtime via the `window.__ENV__` bridge pattern: the server renders `<script>window.__ENV__={GOOGLE_MAPS_API_KEY: process.env.GOOGLE_MAPS_API_KEY}</script>` and client code reads from `window.__ENV__`.

   Checkpoint: Build exits 0, image is tagged locally.

5. **Push (if requested)** -- Append `--push` to the build command, or run `docker push`.

   **Side effect**: `--push` writes to the shared container registry immediately. This is visible to all environments and is not easily reversible without explicit image deletion.

   ```bash
   docker buildx build \
     --platform linux/arm64 \
     -t $IMAGE_REGISTRY/<device-image>:$COMMIT_HASH \
     -f Dockerfile.optimized . --push
   ```

   Checkpoint: Push succeeds; verify with `gcloud artifacts docker images describe $IMAGE_REGISTRY/<image>:$TAG`.

6. **Verify pushed image** -- Confirm the image exists in the registry.
   ```bash
   gcloud artifacts docker images describe $IMAGE_REGISTRY/<image-name>:<tag>
   ```
   Checkpoint: Command returns image metadata including digest.

# Self-check before returning (anti-hallucination, confidence labels, format match)

- [ ] No `NEXT_PUBLIC_*` variables were passed as `--build-arg` (violates 12-factor; use `window.__ENV__` runtime bridge)
- [ ] Registry URL used the `$IMAGE_REGISTRY` variable, not a hardcoded cloud project ID
- [ ] Image tag is a git short hash or semver, never `latest`
- [ ] `--push` side effect was explicitly communicated to the operator before execution
- [ ] If pushed, image existence was verified via `gcloud artifacts docker images describe`
- [ ] Build platform matches the target deployment (ARM64 for RPi/edge, AMD64 for cloud/VM)
- [ ] Base image tag in Dockerfile is pinned to a specific digest or dated tag, not `latest` -- if not, flag it
- [ ] Output matches the build result template format
- [ ] Confidence label attached -- label LOW when build succeeds but push was not verified

# Common mistakes to avoid (DO NOT patterns)

- DO NOT pass `NEXT_PUBLIC_*` as `--build-arg` -- this bakes environment-specific values into the image at `next build` time, making the image usable only in one environment. Use the `window.__ENV__` runtime bridge pattern instead: server injects `<script>window.__ENV__={...}</script>`, client reads from `window.__ENV__`
- DO NOT hardcode the cloud project ID in registry URLs -- use the `$IMAGE_REGISTRY` environment variable. The project ID may differ between contexts (e.g., a shared dev project vs. a numbered production project)
- DO NOT use the `latest` tag -- use git short hash for development or semver for releases
- DO NOT push to the shared registry without confirming the tag and target image -- pushes are visible to all environments
- DO NOT push to the production registry from a local dev machine -- use CI/CD pipeline
- DO NOT forget to set up buildx for cross-platform builds -- `docker build` alone does not support `--platform`
- DO NOT use `gcloud auth login` in CI pipelines -- CI uses Workload Identity Federation (see `gcp-cicd-auth` skill). The `gcloud auth configure-docker` command in this skill's procedure is for local dev auth only

# Escalation (stop-and-ask conditions)

- Stop and ask when: operator wants to push to the production registry from a local machine
- Stop and ask when: build fails with a platform mismatch (e.g., trying to build ARM64 on x86 without QEMU)
- Stop and ask when: `NEXT_PUBLIC_*` build-arg is requested (explain the 12-factor violation and suggest the runtime bridge)
- Stop and ask when: tests have not passed for the code being built
- Stop and ask when: base image in Dockerfile uses `:latest` tag -- flag for pinning

# Examples

<example name="build-and-push-device-service">
## Build and push the device-service image after a telemetry fix

```bash
# Step 1: Authenticate (local dev only -- CI uses WIF)
gcloud auth configure-docker <region>-docker.pkg.dev

# Step 2: Set up buildx
docker buildx create --name multiarch-builder --use 2>/dev/null || docker buildx use multiarch-builder
docker buildx inspect --bootstrap

# Step 3: Build and push
cd <device>
COMMIT_HASH=$(git rev-parse --short HEAD)  # e.g., a1b2c3d

docker buildx build \
  --platform linux/arm64 \
  -t $IMAGE_REGISTRY/<device-image>:$COMMIT_HASH \
  -f Dockerfile.optimized . --push

# Step 4: Verify
gcloud artifacts docker images describe \
  $IMAGE_REGISTRY/<device-image>:$COMMIT_HASH
# Image: <device-image>:a1b2c3d
# Digest: sha256:abc123...
# Platform: linux/arm64
```

Output:
```
## Build Result
Build: <device>
Image: $IMAGE_REGISTRY/<device-image>:a1b2c3d
Platform: linux/arm64
Pushed: yes
Digest: sha256:abc123def456...

### Confidence: HIGH -- push verified via gcloud artifacts describe
```
</example>

<example name="build-main-app-local">
## Build the main-app image (no push, local testing)

```bash
cd apps/<mainApp>
COMMIT_HASH=$(git rev-parse --short HEAD)

# No --build-arg for NEXT_PUBLIC_* variables.
# Client config is injected at runtime via window.__ENV__ bridge.
docker buildx build \
  --platform linux/amd64 \
  -t $IMAGE_REGISTRY/<mainApp>:$COMMIT_HASH \
  -f docker/Dockerfile .
```

Output:
```
## Build Result
Build: <mainApp>
Image: $IMAGE_REGISTRY/<mainApp>:e4f5g6h
Platform: linux/amd64
Pushed: no
Digest: N/A

### Confidence: HIGH -- build succeeded locally
```
</example>

# Failure modes (symptom -> detection -> action)

- **ImagePullBackOff after push**: symptom: runtime pod cannot pull the image -> detect: pod description shows authentication error -> fix: re-authenticate with `gcloud auth configure-docker`, regenerate the docker pull secret via the infrastructure repo's `./files/dockerSecret.sh`
- **buildx not available**: symptom: `docker buildx build` returns "buildx not found" -> detect: error message on command execution -> fix: install buildx plugin or create builder with `docker buildx create --name multiarch-builder --use`
- **ARM64 build slow on x86**: symptom: build takes 10x longer than expected -> detect: build platform is `linux/arm64` but host is x86 -> fix: QEMU emulation is expected to be slow; for faster builds, build on the device directly or use CI runners with native ARM64
- **NEXT_PUBLIC baked into image**: symptom: client-side config shows wrong API key in non-dev environment -> detect: `docker history` shows `NEXT_PUBLIC_*` in build args -> fix: remove `--build-arg`, implement `window.__ENV__` runtime bridge per code-standards
- **Stale base image**: symptom: vulnerability scanner flags known CVEs in base layer -> detect: `docker inspect` shows base image last pulled months ago -> fix: pin base image to a recent digest, rebuild

# Related skills (compose vs defer)

- `deployment` -- **defer**: after docker-build produces the image, deployment handles rolling it out to the runtime environment
- `staging operations` (the project's `<envAlias>-operations` skill) -- **compose**: staging operations consume images from the registry for staging deploys
- `code-standards` -- **compose**: code-standards defines the 12-factor build-once/deploy-anywhere rule that governs how the main-app image must be built
- `gcp-cicd-auth` -- **compose**: for CI pipeline auth via Workload Identity Federation (not `gcloud auth login`)
