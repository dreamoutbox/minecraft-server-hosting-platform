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

- [ ] add user role user/staff/admin

---

- [ ] add server expire datetime. user can extend with payment.

---

- [ ] add new page server console logs.

- [ ] add send command from web console to minecraft server.

---

- [x] File Manager is not showing any files. should show list files in /var/lib/mc-platform/mounts/<server-id>.

---

### fileserver

- [x] new fileserver project. for manage files.

- [x] remove `[DIR]` prefix. make dir show as blue link, and file show as normal text.

- [x] new sub-navbar toolbar for upload file, new file, new folder.

- [x] remove upload file panel & new folder panel

- [x] add ability to edit .txt, .json, .yml .properties on the web. open modal to edit the file and save.

- [x] add button to zip and download folder

- [x] remove rename input text. rename button should open popup to rename.

- [ ] BUG: can't open .yaml, .env file

- [ ] determine if file is text file. and allow open to edit. disallow edit binary file or large file.

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

- [ ] ensure cmd/worker CRON backup all mc server imgs files at /var/lib/mc-platform/images/<server-id>.img to S3 bucket

---

### Debug Utils

- [x] add shell debug script to manually mount /var/lib/mc-platform/images/<server-id>.img for inspection.

- [x] add debug script to list mount status of each dir in /var/lib/mc-platform/mounts/<server-id>.

---

### 11/18/2026

- [ ] test upload file exceed ext4 mount disk should fail
- [ ] show server connect domain. add field public_domain, public_ip to node host tables in postgres
- [ ] add server type for droplet,vps, container
- [ ] data retention 7 days after server expired
- [ ] allow extend server duration if within 7 days after server expired
- [ ] move server create form panel to only admin/staff
- [ ] create server after purchase. place order create server in queue postgres table. and wait for worker to pick up and create server
- [ ] queue worker to create server, stop expired server, delete server data after passed retention days

---

# BACKLOG

- [ ] connect to cloudflare API for minecraft server custom subdomain.

- [ ] add AI chat to manage server.

- [ ] add ability for user to install mods/modpacks from modrinth/curseforge.
