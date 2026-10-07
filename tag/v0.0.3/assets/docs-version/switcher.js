document.addEventListener("change", (event) => {
  const selector = event.target;
  if (!selector.matches("[data-docs-version]")) return;

  const option = selector.options[selector.selectedIndex];
  const root = option.dataset.versionRoot;
  const pagePath = selector.dataset.pagePath || "/";
  const destination = new URL(root.replace(/\/$/, "") + pagePath, window.location.origin);

  fetch(destination, { method: "HEAD" })
    .then((response) => window.location.assign(response.ok ? destination : option.value))
    .catch(() => window.location.assign(option.value));
});
