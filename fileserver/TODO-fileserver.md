# fileserver

- [x] new fileserver project. for manage files.

- [x] remove `[DIR]` prefix. make dir show as blue link, and file show as normal text.

- [x] new sub-navbar toolbar for upload file, new file, new folder.

- [x] remove upload file panel & new folder panel

- [x] add ability to edit .txt, .json, .yml .properties on the web. open modal to edit the file and save.

- [x] add button to zip and download folder

- [x] remove rename input text. rename button should open popup to rename.

- [x] BUG: can't open .yaml, .env file

- [x] determine if file is text file. and allow open to edit. disallow edit binary file or large file.

- [x] add checkbox for select multiple files.

- [x] upload file immediately open browser file dialog. no "Upload File" bootstrap modal.

- [x] file manager limit upload/download files. quota size = 5GB/day (changable with ENV)

- [x] upload file should not own by root:root. add field uid and gid to users table. upload file should be owned by uid:gid to user who uploaded it.

- [x] add dev shell script to inspect sqlite database. show all tables and rows.

- [x] change EnsureUser method and DTO. to RegisterUser.

- [x] the RegisterUser should take Password input.

- [ ] change `Dir` or `Server` naming like in `RevokeServer` `RevokeServerPermissions` to `Drive`

- [ ] move fileserver to new repository.
