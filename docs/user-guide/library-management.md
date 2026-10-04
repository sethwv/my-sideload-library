---
title: Library Management
parent: User Guide
nav_order: 8
---

# Library Management

The optional **Enhancement** settings enrich EPUB metadata and covers with Chaptarr or Hardcover. A library remains usable with metadata found in its EPUB files.

Configure Chaptarr with its base URL and API key. Configure Hardcover with its token. Enable one provider first and confirm results. When both are enabled, Chaptarr matches first; an associated Hardcover ID can fill fields Chaptarr does not provide.

{% include screenshot-pair.html id="enrichment-chaptarr" %}
{% include screenshot-pair.html id="enrichment-hardcover" %}

**Hide unmatched** removes books without a selected-provider match from the library view. **Use Hardcover covers** replaces EPUB covers with provider covers. Use **Reset enrichment** after correcting provider configuration or to retry completed records. It clears derived enrichment state; the normal background processing then considers eligible books again.

Admins can choose **Edit Metadata** from a book panel to correct fields manually, search providers, choose a candidate, look up a Chaptarr path, or select a Hardcover cover. Searches do not persist until **Save**. Server Managers can reach the protected edit route, but the panel's edit icon is currently shown only to Admins.

{% include screenshot-pair.html id="metadata-edit" %}

{: .warning }
Never place provider tokens or API keys in screenshots, support requests, or shared configuration files.
