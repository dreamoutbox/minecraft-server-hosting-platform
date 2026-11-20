# Worker Daemon Deployment Guide

The worker daemon (`cmd/worker`) executes directly on each physical or virtual hosting node. It coordinates local Docker containers and loop-mounted sparse storage volumes for Minecraft servers.

## Hard Requirements

To support disk quotas via loop-mounted sparse ext4 files, the worker daemon requires Linux loop device access and administrative mounting privileges.

### 1. Loop-Mount Capabilities (`--cap-add=SYS_ADMIN --cap-add=MKNOD`)
The worker daemon or its container **must** run with `CAP_SYS_ADMIN` (for `mount`/`umount`) and `CAP_MKNOD` (to create `/dev/loopN` nodes dynamically inside a non-privileged container). These are hard requirements, not optional.

### 2. Device Access
The worker must have access to loop control and loop block devices (block major 7):
- `/dev/loop-control`
- Device cgroup rule `b 7:* rmw` (allows creating and using any `/dev/loopN` node dynamically without pre-mapping `/dev/loop0`..`/dev/loop7`)

### 3. Docker Socket Access
The worker communicates with the host Docker engine via the local UNIX socket:
- `/var/run/docker.sock`

## Docker Container Deployment Example

When deploying the worker inside a Docker container:

```bash
docker run -d \
  --name mc-worker \
  --restart unless-stopped \
  --cap-drop=ALL \
  --cap-add=SYS_ADMIN \
  --cap-add=MKNOD \
  --cap-add=DAC_OVERRIDE \
  --cap-add=CHOWN \
  --cap-add=FOWNER \
  --security-opt apparmor=unconfined \
  --device /dev/loop-control:/dev/loop-control \
  --device-cgroup-rule='b 7:* rmw' \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /var/lib/mc-platform:/var/lib/mc-platform:rshared \
  -e WORKER_PORT=9090 \
  -e WORKER_API_KEY="<provisioned-host-api-key>" \
  -e WORKER_LOOP_DEVICE_ENABLED=true \
  -p 9090:9090 \
  mc-worker:latest
```

> Note: `/var/lib/mc-platform` should use `:rshared` mount propagation if containerized volumes are shared with child Minecraft containers.

## Systemd Service Deployment Example (Bare Host)

When running directly on the Linux host as a systemd service:

```ini
[Unit]
Description=Minecraft Hosting Worker Daemon
After=network.target docker.service
Requires=docker.service

[Service]
Type=simple
User=root
WorkingDirectory=/var/lib/mc-platform
Environment=WORKER_PORT=9090
Environment=WORKER_API_KEY=YOUR_STATIC_SECRET_KEY
Environment=WORKER_BASE_DIR=/var/lib/mc-platform/volumes
Environment=WORKER_MAX_SERVERS=20
Environment=WORKER_LOOP_DEVICE_ENABLED=true
ExecStart=/usr/local/bin/worker
Restart=always
RestartSec=5s

[Install]
WantedBy=multi-user.target
```
