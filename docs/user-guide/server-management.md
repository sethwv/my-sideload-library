---
title: Server Management
parent: User Guide
nav_order: 7
---

# Server Management

Admins and Server Managers use **Admin** for server configuration and maintenance.

## Settings and SMTP

**Setup** controls site name, Public URL, cover width, books per page, session lifetime, password reset, shelf limit, and KEPUB behavior. Database-backed values take precedence after first startup. Set Public URL to the external HTTPS address used in invitation and reset messages.

In **SMTP**, enter the mail provider hostname, port, encryption mode, credentials, and sender identity. Save, send a test email to a controlled address, and verify its links use the configured Public URL. Leaving the SMTP password blank while updating another field retains the stored password.

{% include screenshot-pair.html id="admin-configuration" %}
{% include screenshot-pair.html id="admin-smtp" %}

## Tasks and index maintenance

**Tasks** queues Scan Library, Email digest, and Refresh Chaptarr catalog. Tasks run one at a time and duplicate queued or running jobs are coalesced. The page shows recent history and refreshes while open. Email digest still requires working SMTP.

{% include screenshot-pair.html id="admin-tasks-guide" %}

Use **Clear library and queue scan** only to rebuild the derived index. It removes indexed books, enrichment state, locations, and book-to-shelf memberships, then queues a scan. Shelf definitions remain.

{% include screenshot-pair.html id="admin-server" %}
