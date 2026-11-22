# Node File Server Deployment Guide

The standalone file server (`fileserver/cmd/fileserver`) runs on each hosting node alongside the worker daemon. It manages a local SQLite database for user records and directory permissions (`dir_permissions`) and exposes direct file management over `/var/lib/mc-platform/mounts/<server-id>`.

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `FILESERVER_PORT` | `9100` | HTTP listen port |
| `FILESERVER_SQLITE_PATH` | `/var/lib/mc-platform/fileserver/fileserver.db` | Path to node-local SQLite DB (keep outside mounts dir) |
| `FILESERVER_MOUNTS_DIR` | `/var/lib/mc-platform/mounts` | Base directory containing `<server-id>` mount folders |
| `FILESERVER_API_KEY` | `dev-worker-key` | Bearer API key matching the host's `APIKey` in the main server |
| `FILESERVER_JWT_SECRET` | `dev-fileserver-jwt-secret-change-in-prod` | HMAC secret for single-use login tokens and session cookies |
| `FILESERVER_MAX_UPLOAD_MB` | `2048` | Maximum single upload request size in MB |
| `FILESERVER_DAILY_QUOTA_MB` | `5120` | Daily per-user upload and download quota in MB (`0` = unlimited) |
| `FILESERVER_COOKIE_SECURE` | `false` | Set `true` when serving over HTTPS in production |

## Docker Container Example

```bash
docker run -d \
  --name mc-fileserver \
  --restart unless-stopped \
  -v /var/lib/mc-platform:/var/lib/mc-platform:rshared \
  -v mc_fileserver_data:/data \
  -e FILESERVER_PORT=9100 \
  -e FILESERVER_SQLITE_PATH=/data/fileserver.db \
  -e FILESERVER_MOUNTS_DIR=/var/lib/mc-platform/mounts \
  -e FILESERVER_API_KEY="<host-api-key>" \
  -e FILESERVER_JWT_SECRET="<node-random-jwt-secret>" \
  -e FILESERVER_COOKIE_SECURE=true \
  -p 9100:9100 \
  mc-fileserver:latest
```

## Production Notes

- **TLS / HTTPS:** Terminate TLS with a reverse proxy (Caddy/Nginx) in front of `:9100` so login tokens and `fs_session` cookies are encrypted in transit, and set `FILESERVER_COOKIE_SECURE=true`.
- **Host Registration:** Set `Host.FileServerBaseURL` to the node's public HTTPS URL (for example `https://files-node1.example.com`). Both the browser redirect and `cmd/server` management calls use this URL.
- **Standalone Mode:** `fileserver` has no PostgreSQL dependency; `cmd/server` provisions users and permissions over HTTP using `FILESERVER_API_KEY`.
- **Daily Transfer Quota:** `FILESERVER_DAILY_QUOTA_MB` (default `5120`, i.e., 5 GB) caps each user's daily uploads and daily downloads separately per UTC day (`00:00 UTC` reset). Transfers exceeding the remaining quota return HTTP `429 Too Many Requests`. Set to `0` to disable quota enforcement.
