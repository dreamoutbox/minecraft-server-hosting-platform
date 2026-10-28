# TODO

- [ ] add user role user/staff/admin

- [ ] add server expire datetime. user can extend with payment.

- [ ] add server console logs view.

- [ ] add send command to server.

- [ ] File Manager is showing server files.

- [ ] move World Backups panel to new html page.

- [ ] add settings html page. move Danger Zone panel to settings.

- [ ] create and reuse navbar for server details. make navigation links visible based on user role.

- [ ] move Host Worker Status, Host Migration to new html page. only allow/visible to staff and admin.

- [ ] make create_server/delete_server/backup/restore become queue. don't make user wait when create server. queue also add retry ability.

- [ ] log server create/stop/delete/backup/restore with success or error in postgresql db.

- [ ] ensure worker stop/compose down. umount ext4 volumes. remove containers.

- [ ] make sure deleted server also delete ext4 image file. and make sure /var/lib/mc-platform/mounts/server-id is empty.

# BACKLOG

- [ ] add AI chat to manage server.
