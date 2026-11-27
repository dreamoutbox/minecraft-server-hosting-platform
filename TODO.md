# TODO

- [x] ensure worker stop/compose down. umount ext4 volumes. remove containers.

- [x] make sure user delete server. also delete ext4 image file. and make sure /var/lib/mc-platform/mounts/<server-id> is empty.

- [x] can't stop mc server if worker stop and start. worker stop all mc server containers before worker stop -> worker start -> user request stop mc server from web -> stop command can't find container.
web error:
```
worker stop container failed: stop container failed with code 500: {"error":"failed stopping container 891874f554ad122544ca5f412f6b2e2c176450f2da788cd8613c20f1c0aa81b4: Error response from daemon: No such container: 891874f554ad122544ca5f412f6b2e2c176450f2da788cd8613c20f1c0aa81b4"}
```

---

- [x] add script to reset db data.

- [x] add script to unmount and remove data at /var/lib/mc-platform/mounts/.

- [x] add ./dev-reset-wrapper to reset db and /var/lib/mc-platform/mounts/.

---

- [x] add user role user/staff/admin

---

- [x] add new page server console logs.

- [x] add send command from web console to minecraft server.

---

- [x] ~~File Manager is not showing any files. should show list files in /var/lib/mc-platform/mounts/<server-id>.~~ use fileserver now.

---

- [x] move World Backups panel to new page.

- [x] add settings page. 

- [x] move Danger Zone panel to settings.

- [x] create and reuse sub-navbar for server details. make navigation links visible based on user role.

- [x] move "Host Worker Status panel" & "Host Migration panel" to new page. only allow/visible to staff and admin.

- [x] user can choose vanilla/paper/fabric/forge server jar, and server version in server settings page.

---

- [x] make create_server/delete_server/backup/restore become queue. don't make user wait when create server. queue also add retry ability.

- [x] log server create/stop/delete/backup/restore with success or error in postgresql db.

---

## Debug Utils

- [x] add shell debug script to manually mount /var/lib/mc-platform/images/<server-id>.img for inspection.

- [x] add debug script to list mount status of each dir in /var/lib/mc-platform/mounts/<server-id>.

---

## 11/18/2026

- [x] test upload file exceed ext4 mount disk should fail. 
    - [x] add new plan with 1 gb disk
    - [x] create random large file 1.1 gb
    - [x] test upload file
    - [x] expected fail.

- [x] show server connect domain on web dashboard. add field public_domain, public_ip to node host tables in postgres

- [x] add server expire datetime. user can extend with payment.
- [x] prevent user from starting server if server is expired
- [x] data retention 7 days after server expired
- [x] allow extend server duration if within 7 days after server expired

- [x] move server create form panel to only admin/staff
- [x] add stripe webhooks to handle user payment
- [x] create server after purchase. place order create server in queue postgres table. and wait for worker to pick up and create server

- [x] new background worker to create server from queue, stop expired server, delete server data after passed retention days

- [x] change "Create Your First Server" to "Buy Server" if user is not admin/staff

- [x] restore from backup failed: failed importing volume contents on worker: import volume failed with code 500: {"error":"failed creating file /var/lib/mc-platform/mounts/1/.install-vanilla.env: open /var/lib/mc-platform/mounts/1/.install-vanilla.env: permission denied"}

- [x] admin navbar. new menu (with search and pagination):
    - [x] list all servers 
    - [x] orders
    - [x] payment_records
    - [x] server_jobs
    - [x] server_operation_logs
    - [x] plans (with CRUD)

- [x] add script to reset dev seaweedfs s3 bucket. also add to dev-reset-wrapper.sh.

---

## 11/20/2026

- [x] change all env in compose to use `${VAR:-default}` (VAR or default) syntax

- [x] test deploy in real VPS.

- [x] make worker update `hostname` `public_domain` `public_ip` `worker_base_url` `file_server_base_url` `api_key` `max_servers` when env change.

- [x] add button to manually retry failed server_jobs

- [x] add copy address to "Connect Address" and "Connect IP Address"

- [x] use `USE_MEOWICE_FLAGS` for mc container

---

## 11/22/2026

- [x] change env `RETENTION_DAYS` to `SERVER_EXPIRED_DATA_RETENTION_DAYS`

- [x] add backup env config: `CRON_MC_SERVER_DATA_BACKUP` (default `0 5 * * *`, local TZ), presigned URL TTL (default `1h`), and max backups per server (default `8`)

- [x] switch user backups (`backup_records`) to `tar.gz` of mounted files (`/var/lib/mc-platform/mounts/<server-id>`) uploaded/restored by `cmd/worker` via presigned PUT/GET URLs (`cmd/worker` never accesses S3 directly)

- [x] implement CRON node ext4 `.img` backups (separate `server_image_backups` table):
    - [x] `jobworker` CRON enqueues jobs in `server_jobs` for all provisioned servers (`running` + `stopped`; skip `pending`/`provisioning`/`migrating`/`error`)
    - [x] quiesce running servers (`save-all` + `sync`), `tar.gz` `/var/lib/mc-platform/images/<server-id>.img`, split compressed stream into <=1 GiB parts if over 1 GiB, and upload via presigned PUT URLs from `jobworker`

- [x] enforce backup retention limit: keep last N (default 8) completed backups per server independently for both `backup_records` and `server_image_backups` (prune older S3 objects + DB rows)

- [x] add admin-only `.img` restore endpoint on `cmd/apiserver` + shell script (`curl` apiserver -> `jobworker` generates presigned GET URLs -> `cmd/worker` downloads, merges/uncompresses parts, and restores `.img`)

- [x] assert CRON mc servers backup, restore, and retention pruning work end-to-end

- [x] script to manually trigger admin ext4 imgs backup and restore.

- [x] fix admin can't manage files on another user server. error "unauthorized access to server". should invoke to fileserver and give access to admin user for that server. when click on manage file tab

- [x] fix admin can't backup, restore, send command to server, change settings, delete server on another user server. error "unauthorized access to server"

- [x] move extend server button to new panel in server settings page

- [x] web dashboard add alert if user server is about to expire. add link to navigate to server settings page > extend server panel. 

---

## 11/27/2026

- [x] rename `cmd/worker` to `cmd/nodemanager`

- [x] extract `cmd/nodemanager` code in `./internal` to new dir at `./nodemanager`

- [x] extract `cmd/jobworker` code in `./internal` to new dir at `./jobworker`

- [x] if any of these new dir shared code in `./internal`, the shared code to `./shared`

- [x] separate routes in `internal/dashboard/dashboard.go` to it own file. one route per one go file. don't mix everything in just one file. separate by category like: users, billings, servers, etc. like internal/users/login.go, internal/users/register.go, internal/billings/buy_server.go, internal/mcservers/create-server.go

- [x] move each jobs in internal/jobs/queue.go to it own file. one job per one go file. example: jobworker/jobs/process_expired_servers.go, jobworker/jobs/cleanup_retention_servers.go, jobworker/jobs/enqueue_all_image_backups.go, etc...

- [ ] move internal/middleware to shared/gin_middlewares. one middleware per one go file.

---

# BACKLOG

- [ ] move shared/authkit to new git repo

- [ ] add button to kill server. for unresponsive mc server.

- [ ] add ability for user to install mods/modpacks from modrinth/curseforge.

- [ ] connect to cloudflare API for minecraft server custom subdomain.

- [ ] add AI chat to manage user mcservers.

- [ ] add server_instance_type for vps, container to servers tables
