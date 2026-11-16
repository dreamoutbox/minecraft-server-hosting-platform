# TODO

- [ ] ensure worker stop/compose down. umount ext4 volumes. remove containers.

- [ ] make sure deleted server also delete ext4 image file. and make sure /var/lib/mc-platform/mounts/server-id is empty.

---

- [x] add script to reset db data.

- [x] add script to unmount and remove data at /var/lib/mc-platform/mounts/.

- [x] add ./dev-reset-wrapper to reset db and /var/lib/mc-platform/mounts/.

---

- [ ] add user role user/staff/admin

---

- [ ] add server expire datetime. user can extend with payment.

---

- [ ] add server console logs view.

- [ ] add send command from web console to minecraft server.

---

- [ ] File Manager is showing server files.

---

- [ ] move World Backups panel to new page.

- [ ] add settings page. move Danger Zone panel to settings.

- [ ] create and reuse navbar for server details. make navigation links visible based on user role.

- [ ] move Host Worker Status panel, Host Migration panel to new page. only allow/visible to staff and admin.

---

- [ ] make create_server/delete_server/backup/restore become queue. don't make user wait when create server. queue also add retry ability.

- [ ] log server create/stop/delete/backup/restore with success or error in postgresql db.

---

# BACKLOG

- [ ] connect to cloudflare API for minecraft server custom subdomain.

- [ ] add AI chat to manage server.
