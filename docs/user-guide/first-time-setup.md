---
title: First Time Setup
parent: User Guide
nav_order: 1
---

# First Time Setup

This guide starts after the server is deployed and you can open its address. See [Deployment](../../deployment/) only if you still need platform, storage, or server configuration help.

## 1. Create the first administrator

1. Open the library address in your browser.
2. On a new installation, select **Set up** from the sign-in screen.
3. Choose a username and a strong password for the first administrator.
4. Select **Create administrator**. The library signs you in and opens the Library.

{% include screenshot-pair.html id="first-admin-setup" %}

## 2. Confirm that books are available

1. Look for books in the Library.
2. If it is empty, wait for the startup scan to finish.
3. To run another scan, open **Admin**, select **Tasks**, then run **Scan Library**.
4. When books appear, continue with [Browsing](../browsing/) and [Downloading](../downloading/).

{% include screenshot-pair.html id="admin-tasks-guide" %}

## 3. Add readers when you are ready

1. Open **Admin** and select **Users**.
2. Choose **Add user** to set a username, password, and role directly.
3. Configure the Public URL and SMTP first if you prefer to send invitations.
4. Continue with [User Management](../user-management/) for roles, invitations, and permissions.

{% include screenshot-pair.html id="admin-add-user" %}

The one-time setup screen is unavailable after the first account exists. Use the [Admin CLI](../admin-cli/) if all administrator access is later lost.
