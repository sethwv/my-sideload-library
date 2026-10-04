---
title: Admin CLI
parent: User Guide
nav_order: 9
---

# Admin CLI

The local `sideload-library admin` command is a break-glass tool for the server operator. Use it when the web interface cannot restore access, such as when every administrator password is lost. It changes the same `users.db` used by the application, so stop the server before running it.

## Reset a user's password

Run the command with the application's `DATA_DIR`, account username, and a replacement password.

```sh
read -rsp "Password: " password; echo
DATA_DIR=/var/lib/sideload-library sideload-library admin reset-password admin "$password"
unset password
```

The command does not start the web server or print the password. Use a private terminal and avoid placing passwords directly in shell history, process managers, or shared scripts.

## Create a recovery user

Create an account when no existing administrator can sign in. The optional role is one of `member`, `user_manager`, `server_manager`, or `admin`; use `admin` only for a trusted recovery account.

```sh
read -rsp "Password: " password; echo
DATA_DIR=/var/lib/sideload-library sideload-library admin create-user recovery-admin "$password" admin
unset password
```

Use a private terminal and avoid placing passwords directly in shell history, process managers, or shared scripts. After access is restored, remove temporary recovery accounts from **Admin** if they are no longer needed.

{: .warning }
The command has full local access to the user database. Restrict server and data-directory access to trusted operators, and always stop the service before running it.
