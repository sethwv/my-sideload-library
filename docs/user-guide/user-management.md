---
title: User Management
parent: User Guide
nav_order: 6
---

# User Management

Admins and User Managers open **Admin**, then **Users**, to manage library accounts.

| Role | Access |
|---|---|
| Member | Browse, download, and use personal account features. |
| User Manager | Member access plus user, role, and invitation management. |
| Server Manager | Member access plus server, email, task, integration, and metadata management. |
| Admin | All capabilities. |

## Create or invite a user

Use **Add user** to set a username, password, optional email address, and role. Give the initial password through a private channel. Use **Invite by email** when SMTP and a Public URL are configured and tested. The invitation creates a pending account and sends a password-setting link that expires after seven days; resend it from the user's management panel if needed.

{% include screenshot-pair.html id="admin-add-user" %}
{% include screenshot-pair.html id="admin-invite-user" %}

## Manage permissions safely

Use **Manage** to change a role, email address, password, account enabled state, or delete an account. Disabled accounts retain data but cannot sign in or use bookmark, reset, or invitation links. The final enabled Admin cannot be disabled, demoted, or deleted.

Only an Admin can set individual Allow or Deny permission overrides. User Managers can assign roles but cannot alter that override matrix. Use the least-privileged role that allows the work.

{% include screenshot-pair.html id="admin-manage-user" %}
