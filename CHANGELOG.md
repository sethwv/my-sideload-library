# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- Added administrator controls to disable accounts and configurable access capabilities for individual users.
- Added private, shared, and public shelves with member management, configurable creation limits, and administrator shelf management.
- Added quick Favourites, Recent, and full shelf-picker controls to book details, including shelf downloads in EPUB and KEPUB formats.
- Added self-service email settings.

### Changed

- Consolidated password, digest, bookmark-link, and available email settings under the account pages.
- Made password reset available only when an administrator enables it after configuring SMTP and a public URL.
- Updated book cards with cover title overlays and abbreviated author names when space is limited.

### Security

- Revoked active sessions when an administrator disables an account and blocked disabled accounts from authentication flows.
- Added CSRF protection for authenticated state-changing requests.
- Rate-limited failed login attempts and password-reset requests.
- Hardened redirect handling and remote cover fetching.

### Fixed

- Restored Kobo quick shelf controls and shelf-selection icons, and prevented background search fields from receiving focus while a modal is open.
- Allowed session cookies over HTTP for trusted LAN deployments.
- Displayed background task failures in the administrator task list.
- Supported scoped Hardcover API keys and paginated large Chaptarr catalog refreshes.
- Routed the administrator menu to the first page available to the signed-in user's permissions.

## [v0.0.3] - 2026-10-02

### Added

- Added a first-run setup flow for creating the initial administrator account.
- Added background task administration for library scans, scheduled email digests, and Chaptarr catalog refreshes.
- Added configurable KEPUB downloads, including an option to write enriched series metadata for Kobo NickelSeries imports.
- Added standalone macOS downloads for Apple Silicon and Intel Macs.

### Changed

- Renamed the project to my-sideload-library and updated its application, container, and release identities.
- Reorganized administrator configuration into dedicated server, email, and integration pages.
- Cached Chaptarr catalogs locally and refresh them on a schedule before using Hardcover fallback matches.

### Fixed

- Made the server available while the initial library scan runs in the background.

## [v0.0.2] - 2026-09-16

### Fixed

- Made duplicate-book consolidation and canonical-record promotion atomic to prevent interrupted scans from leaving inconsistent library data.

### Security

- Hardened redirects, sessions, SMTP delivery, and remote cover fetching.
- Replaced a vulnerable image-processing dependency.

## [v0.0.1] - 2026-09-15
*(Initial Release, Not Exhaustive)*

### Added

- Added a self-hosted EPUB library with indexing, browsing, search, EPUB downloads, and on-demand KEPUB conversion.
- Added author and series browsing, release-date sorting, book details, and shareable book links.
- Added per-user shelves, favorites, and configurable appearance modes.
- Added multi-user accounts, role-based administration, invitations, password resets, and bookmarkable e-reader login links.
- Added optional Hardcover and Chaptarr metadata enrichment, including cover replacement and manual metadata editing.
- Added Docker images and standalone Windows and Debian binary releases.

### Changed

- Allowed multiple library directories to be indexed as one library.
- Improved navigation, pagination, and touch-friendly layouts for Kobo e-reader browsers.

### Fixed

- Improved modal behavior, toolbar layout, and card rendering on Kobo browsers.
- Made metadata enrichment precedence deterministic and improved chained Chaptarr and Hardcover matching.

### Security

- Restricted bookmarkable login sessions to library viewing and shelves.
- Required an explicit public URL before sending password reset and invitation emails.
