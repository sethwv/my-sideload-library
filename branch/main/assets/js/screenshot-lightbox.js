(() => {
  let trigger;
  let lightbox;

  function close() {
    if (!lightbox) return;
    lightbox.remove();
    lightbox = undefined;
    trigger?.focus();
  }

  function open(link) {
    trigger = link;
    lightbox = document.createElement("div");
    lightbox.className = "screenshot-lightbox";
    lightbox.setAttribute("role", "dialog");
    lightbox.setAttribute("aria-modal", "true");
    lightbox.setAttribute("aria-label", link.dataset.screenshotAlt);

    const image = document.createElement("img");
    image.src = link.dataset.screenshotSrc;
    image.alt = link.dataset.screenshotAlt;

    const button = document.createElement("button");
    button.type = "button";
    button.className = "screenshot-lightbox-close";
    button.textContent = "Close image";
    button.addEventListener("click", close);

    lightbox.append(button, image);
    lightbox.addEventListener("click", (event) => {
      if (event.target === lightbox) close();
    });
    document.body.append(lightbox);
    button.focus();
  }

  document.addEventListener("click", (event) => {
    const link = event.target.closest(".screenshot-lightbox-trigger");
    if (!link) return;
    event.preventDefault();
    open(link);
  });

  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") close();
  });
})();
