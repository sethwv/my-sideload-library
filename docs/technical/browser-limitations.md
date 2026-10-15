---
title: Browser Limitations
parent: Technical
nav_order: 1
---

# Browser Limitations

The primary target is Kobo's built-in browser, a roughly 2015-era QtWebKit engine. The behavior below is confirmed on project hardware. Modern browser testing is useful for regressions, but it does not certify an e-reader browser.

## Layout and styling

- **No Flexbox or CSS Grid.** The application uses CSS2.1-era inline blocks and tables for layout. Do not assume modern [CSS Grid](https://developer.mozilla.org/en-US/docs/Web/CSS/CSS_grid_layout) support on a Kobo device.
- **No `aspect-ratio`.** Cover ratios use a padding-based wrapper instead.
- **No CSS custom properties.** Declarations using `var()` are discarded by the target engine, so application styles use literal values. See MDN's [custom properties guide](https://developer.mozilla.org/en-US/docs/Web/CSS/Using_CSS_custom_properties).
- **Child scrollbars can hide their own text.** The application relies on normal page scrolling and avoids `overflow: auto` or `overflow: scroll` on child elements.

## JavaScript and interaction

- **No Web Storage.** `localStorage` and `sessionStorage` are unavailable, so appearance preference uses a cookie. See the [Web Storage API](https://developer.mozilla.org/en-US/docs/Web/API/Web_Storage_API).
- **Use ES5 syntax and `XMLHttpRequest`.** Modern syntax and the [Fetch API](https://developer.mozilla.org/en-US/docs/Web/API/Fetch_API) are not safe assumptions. The small interactions that require asynchronous requests use `XMLHttpRequest`.
- **CSS `:target` is unreliable for modals.** Book and account modals use explicit click handlers rather than the [`:target` pseudo-class](https://developer.mozilla.org/en-US/docs/Web/CSS/:target).
- **Native select popups are fragile.** Appearance controls use buttons instead of a native select.

## Navigation and caching

- **GET responses can appear stale.** The server sends `Cache-Control: no-cache` for HTML pages to avoid aggressive old-browser caching of search and sort URLs.
- **Hardware testing remains necessary.** There is no faithful QtWebKit simulator. Test CSS and JavaScript changes on a real device before treating them as compatible.

These constraints apply to the application served by my-sideload-library. The public documentation site may use modern browser features such as the screenshot lightbox.
