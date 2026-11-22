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

### Debug Utils

- [x] add shell debug script to manually mount /var/lib/mc-platform/images/<server-id>.img for inspection.

- [x] add debug script to list mount status of each dir in /var/lib/mc-platform/mounts/<server-id>.

---

### 11/18/2026

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

- [ ] add button to kill server. for unresponsive mc server.

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

- [ ] add ability for user to install mods/modpacks from modrinth/curseforge.

---

### 11/20/2026

- [x] change all env in compose to use `${VAR:-default}` (VAR or default) syntax

- [x] test deploy in real VPS.

- [x] make worker update `hostname` `public_domain` `public_ip` `worker_base_url` `file_server_base_url` `api_key` `max_servers` when env change.

- [x] add button to manually retry failed server_jobs

- [x] add copy address to "Connect Address" and "Connect IP Address"

- [x] use `USE_MEOWICE_FLAGS` for mc container

---

### 11/22/2026

- [x] change env `RETENTION_DAYS` to `SERVER_EXPIRED_DATA_RETENTION_DAYS`

- [x] add backup env config: `CRON_MC_SERVER_DATA_BACKUP` (default `0 5 * * *`, local TZ), presigned URL TTL (default `1h`), and max backups per server (default `8`)

- [x] switch user backups (`backup_records`) to `tar.gz` of mounted files (`/var/lib/mc-platform/mounts/<server-id>`) uploaded/restored by `cmd/worker` via presigned PUT/GET URLs (`cmd/worker` never accesses S3 directly)

- [x] implement CRON node ext4 `.img` backups (separate `server_image_backups` table):
    - [x] `jobworker` CRON enqueues jobs in `server_jobs` for all provisioned servers (`running` + `stopped`; skip `pending`/`provisioning`/`migrating`/`error`)
    - [x] quiesce running servers (`save-all` + `sync`), `tar.gz` `/var/lib/mc-platform/images/<server-id>.img`, split compressed stream into <=1 GiB parts if over 1 GiB, and upload via presigned PUT URLs from `jobworker`

- [x] enforce backup retention limit: keep last N (default 8) completed backups per server independently for both `backup_records` and `server_image_backups` (prune older S3 objects + DB rows)

- [x] add admin-only `.img` restore endpoint on `cmd/apiserver` + shell script (`curl` apiserver -> `jobworker` generates presigned GET URLs -> `cmd/worker` downloads, merges/uncompresses parts, and restores `.img`)

- [x] assert CRON mc servers backup, restore, and retention pruning work end-to-end

---

# BACKLOG

- [ ] connect to cloudflare API for minecraft server custom subdomain.

- [ ] add AI chat to manage server.

- [ ] add server_instance_type for vps, container to servers tables
