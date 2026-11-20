# Postmortem: Static Loop Device Mapping in Worker Container

**Date:** 2026-11-20
**Severity:** Medium
**Status:** Resolved

## Summary
The `mc_worker` service in `docker-compose.app.yml` statically mapped only `/dev/loop0` through `/dev/loop7` and capped `WORKER_MAX_SERVERS` at 8, causing loop-mounted ext4 server provisioning to fail beyond 8 servers or whenever host loop indices 0–7 were occupied or absent. Replaced the static mappings with `CAP_MKNOD` and on-demand `/dev/loopN` node creation in the worker Go codebase, scoped by `device_cgroup_rules: ["b 7:* rmw"]`.

## Impact
- Prevented scaling `mc_worker` past 8 concurrent ext4-backed Minecraft servers without manual Compose edits.
- Caused `mount -o loop` failures even below 8 servers on hosts where `/dev/loop0`–`/dev/loop7` were already held by host processes (e.g., Snap packages or stale Docker Desktop WSL2 bind mounts, as previously observed in `post-mortem/2026-11-18-fileserver-large-upload-slowdown.md`).
- Risked container startup failure on minimal Linux hosts where `/dev/loop0`–`/dev/loop7` were not pre-created in host `/dev`.

## Root Cause
- Non-privileged Docker containers mount a private `tmpfs` at `/dev` rather than sharing the host `devtmpfs`.
- Although `device_cgroup_rules: ["b 7:* rmw"]` already permitted kernel access and `mknod` for all loop devices (block major 7, any minor `*`), `util-linux`'s `mount -o loop` allocates a free host-global index via `/dev/loop-control` (`LOOP_CTL_GET_FREE`) and expects udev to populate `/dev/loopN` rather than calling `mknod` itself.
- Working around this by pre-mapping `/dev/loop0`–`/dev/loop7` in `docker-compose.app.yml` tied container capacity to 8 fixed host-global indices and broke cleanup after container restarts if a mount used an unmapped loop index.

## Detection
Caught during code review of `docker-compose.app.yml` (lines 58–82) while planning for >8 Minecraft servers per worker node.

## Resolution
- **Compose & docs (`docker-compose.app.yml`, `docs/worker-deployment.md`)**:
  - Added `MKNOD` to `worker.cap_add` while keeping `cap_drop: [ALL]` and `device_cgroup_rules: ["b 7:* rmw"]` so `mknod` and read/write remain restricted to loop block devices (`7:*`).
  - Removed hardcoded `/dev/loop0`–`/dev/loop7` entries from `devices`, keeping only `/dev/loop-control:/dev/loop-control`.
  - Parameterized `WORKER_MAX_SERVERS` as `${WORKER_MAX_SERVERS:-20}` and documented the `"b 7:* rmw"` cgroup rule inline.
- **Worker loop allocation & node creation (`internal/worker/loop_linux.go`, `internal/worker/loop_other.go`, `internal/worker/volume.go`)**:
  - Implemented `allocateFreeLoopIndex()` (`LOOP_CTL_GET_FREE` ioctl on `/dev/loop-control`) and `ensureLoopDeviceNode()` (`mknod` block device `7:<index>` with `0660`, handling `EEXIST` races).
  - Updated `mountLoopImage` to allocate a free index, ensure `/dev/loopN` exists, and mount via `mount -o loop=/dev/loopN` with retries against host loop contention.
  - Updated `unmountDir` (using `/proc/self/mountinfo` source parsing) and `detachLoopDevices` to ensure `/dev/loopN` exists before detaching, preserving clean teardown across container restarts.
- **Verification (`internal/worker/worker_test.go`, `internal/worker/loop_linux_test.go`)**:
  - Added unit tests for `parseLoopDeviceIndex`, `parseMountInfoLine`, and `ensureLoopDeviceNodeAt`; verified with `go test`, `go vet`, non-Linux cross-build, and `docker compose config --quiet`.

## Lessons Learned / Action Items
- [ ] Rebuild and redeploy the `mc_worker` image (`Dockerfile.worker`) before running with the updated `docker-compose.app.yml`.
- [ ] Run an end-to-end test provisioning >8 concurrent servers in a containerized `mc_worker` (including a container restart + unmount cycle) to verify dynamic `/dev/loopN` creation and cleanup on live Linux/WSL2 hosts.
- [ ] Avoid mapping fixed host-global kernel device indices (`/dev/loop0..N`) into containers when a control device (`/dev/loop-control`) and scoped `device_cgroup_rules` can allocate them dynamically.
