// Plain ES5, loaded on every page. Shows/hides modals via direct inline
// style, not CSS :target because :target proved unreliable on the actual target
// device (Kobo's QtWebKit) even though it's a very old, otherwise-safe CSS
// selector. No fetch, no arrow functions, no let/const: just
// getElementById and style.display, the most basic DOM API there is.
//
// The dark backdrop (#modal-backdrop, defined once in layout.html) and the
// per-book box (.modal-overlay) are separate elements on purpose: the
// backdrop is position:fixed so it always covers the current viewport with
// zero size computation needed, while .modal-overlay is position:absolute
// and sized to fit only its own content, so a tall box (long description)
// just scrolls with the rest of the page instead of needing an inner
// scroll region (this engine's inner-element scrollbars are known-buggy).
// Since .modal-overlay's markup lives wherever its book's card happens to
// be in the DOM, openModal positions it at the current scroll offset so it
// appears where the user is looking rather than wherever that DOM node
// naturally falls.
var openModalId = null;

function setLibrarySearchDisabled(disabled) {
  var search = document.getElementById("q");
  var submit = document.getElementById("search-submit");
  if (search) {
    if (disabled && search.blur) {
      search.blur();
    }
    search.disabled = disabled;
  }
  if (submit) {
    submit.disabled = disabled;
  }
}

function openModal(id) {
  var el = document.getElementById(id);
  var backdrop = document.getElementById("modal-backdrop");
  if (el) {
    el.style.top = (window.pageYOffset || document.documentElement.scrollTop || 0) + "px";
    el.style.display = "block";
    openModalId = id;
    setLibrarySearchDisabled(true);
  }
  if (window.syncThemeModeControl) {
    window.syncThemeModeControl();
  }
  if (backdrop) {
    backdrop.style.display = "block";
  }
  return false;
}

function closeModal(id) {
  var el = document.getElementById(id);
  if (el) {
    el.style.display = "none";
  }
  var backdrop = document.getElementById("modal-backdrop");
  if (backdrop) {
    backdrop.style.display = "none";
  }
  openModalId = null;
  setLibrarySearchDisabled(false);
  return false;
}

function openShelfPicker(bookId) {
  closeModal("book-" + bookId);
  return openModal("book-" + bookId + "-shelves");
}

function backToBook(bookId) {
  closeModal("book-" + bookId + "-shelves");
  return openModal("book-" + bookId);
}

// Tapping the dark backdrop, or the per-book overlay's own padding area
// (outside .modal-box but inside .modal-overlay), closes whichever modal
// is open. A click inside .modal-box always has some descendant element
// as e.target, never these two elements, so this distinguishes "outside"
// from "inside" without wiring up each modal individually.
document.onclick = function (e) {
  e = e || window.event;
  var target = e.target || e.srcElement;
  if (!target) {
    return;
  }
  if (target.id === "modal-backdrop") {
    closeModal(openModalId);
    return;
  }
  if ((" " + target.className + " ").indexOf(" modal-overlay ") > -1) {
    closeModal(target.id);
  }
};

// Shelf toggle, instant, no page reload. Uses XMLHttpRequest rather than
// fetch(), which is almost certainly absent on this engine and throws
// synchronously before a .catch() can run (confirmed by an earlier version
// of this shelf-toggle interaction silently breaking on-device);
// XMLHttpRequest is a much older, more broadly supported API. isViewing is
// true only for the shelf currently being browsed (e.g. tapping "Favourites"
// while on the Favourites page). Since every book on that page is
// guaranteed already on that shelf, tapping it always means "remove," so on
// success the whole card and its modal are dropped from the page instead of
// just flipping the checkmark.
function setShelfButtonState(btn, onShelf) {
  var hasClass = (" " + btn.className + " ").indexOf(" is-on ") > -1;
  if (onShelf && !hasClass) {
    btn.className += " is-on";
  } else if (!onShelf && hasClass) {
    btn.className = (" " + btn.className + " ").replace(" is-on ", " ").replace(/^\s+|\s+$/g, "");
  }
  var glyphs = btn.getElementsByTagName("svg");
  for (var i = 0; i < glyphs.length; i++) {
    if ((" " + glyphs[i].getAttribute("class") + " ").indexOf(" shelf-check ") > -1) {
      glyphs[i].style.display = onShelf ? "inline-block" : "none";
      glyphs[i].style.visibility = onShelf ? "visible" : "hidden";
    }
  }
}

function setQuickActionsLayout(btn, showRecent) {
  var quickActions = btn;
  while (quickActions && quickActions.tagName !== "TABLE") {
    quickActions = quickActions.parentNode;
  }
  if (!quickActions) {
    return;
  }
  var quickButtons = quickActions.getElementsByTagName("button");
  var hasMore = false;
  for (var i = 0; i < quickButtons.length; i++) {
    if ((" " + quickButtons[i].className + " ").indexOf(" shelf-quick-more ") > -1) {
      hasMore = true;
      break;
    }
  }
  quickActions.className = "shelf-quick-actions shelf-quick-actions-" + (showRecent ? (hasMore ? "three" : "two") : (hasMore ? "favorites-more" : "one"));
}

function syncShelfControls(bookId, shelfId, onShelf, recent) {
  var buttons = document.getElementsByTagName("button");
  var i;
  for (i = 0; i < buttons.length; i++) {
    if (buttons[i].getAttribute("data-book-id") === String(bookId) && buttons[i].getAttribute("data-shelf-id") === String(shelfId)) {
      setShelfButtonState(buttons[i], onShelf);
    }
  }
  var recentButton = document.getElementById("book-" + bookId + "-recent");
  var recentForm = document.getElementById("book-" + bookId + "-recent-form");
  var recentSlot = document.getElementById("book-" + bookId + "-recent-slot");
  if (!recentButton || !recentForm) {
    return;
  }
  if (!recent) {
    if (recentSlot) {
      recentSlot.style.display = "none";
    }
    setQuickActionsLayout(recentButton, false);
    return;
  }
  recentButton.setAttribute("data-shelf-id", recent.id);
  setShelfButtonState(recentButton, recent.onShelf);
  recentForm.action = "/books/" + bookId + "/shelves/" + recent.id;
  var labels = recentButton.getElementsByTagName("span");
  for (i = 0; i < labels.length; i++) {
    if ((" " + labels[i].className + " ").indexOf(" shelf-label-name ") > -1) {
      labels[i].textContent = recent.name;
      break;
    }
  }
  if (recentSlot) {
    recentSlot.style.display = "table-cell";
    recentButton.disabled = false;
    recentButton.setAttribute("type", "submit");
  }
  setQuickActionsLayout(recentButton, true);
}

function toggleShelfForButton(btn, isViewing) {
  return toggleShelf(btn.getAttribute("data-book-id"), btn.getAttribute("data-shelf-id"), isViewing);
}

function toggleShelf(bookId, shelfId, isViewing) {
  var xhr = new XMLHttpRequest();
  xhr.open("POST", "/books/" + bookId + "/shelves/" + shelfId, true);
  xhr.setRequestHeader("Accept", "application/json");
  xhr.onreadystatechange = function () {
    if (xhr.readyState !== 4) {
      return;
    }
    if (xhr.status < 200 || xhr.status >= 400) {
      return;
    }
    if (isViewing) {
      var overlay = document.getElementById("book-" + bookId);
      var card = document.getElementById("card-" + bookId);
      // The backdrop is shared/global now, not part of this overlay. Removing
      // just the overlay would leave the backdrop up with nothing on top of it.
      closeModal("book-" + bookId);
      if (overlay && overlay.parentNode) {
        overlay.parentNode.removeChild(overlay);
      }
      if (card && card.parentNode) {
        card.parentNode.removeChild(card);
      }
    } else {
      var state;
      try {
        state = JSON.parse(xhr.responseText);
      } catch (err) {
        return;
      }
      syncShelfControls(bookId, shelfId, state.onShelf, state.recent);
    }
  };
  xhr.send();
  return false;
}

// Direct-link support: a book card's cover-link href always includes
// ?book=<id> (see library.html/render.go's withQueryParam), so sharing or
// bookmarking that URL reopens the same modal. window.onload (not
// addEventListener/DOMContentLoaded, the plainest, oldest-engine-safe
// hook) checks for that param on page load and opens the matching modal.
// No URLSearchParams (not assumed present on this engine, same reasoning
// as avoiding fetch() elsewhere in this file): parsed by hand instead.
function getQueryParam(name) {
  var search = window.location.search;
  if (!search || search.charAt(0) !== "?") {
    return null;
  }
  var pairs = search.substring(1).split("&");
  for (var i = 0; i < pairs.length; i++) {
    var kv = pairs[i].split("=");
    if (decodeURIComponent(kv[0]) === name) {
      return kv.length > 1 ? decodeURIComponent(kv[1].replace(/\+/g, " ")) : "";
    }
  }
  return null;
}

window.onload = function () {
  var bookId = getQueryParam("book");
  if (bookId) {
    openModal("book-" + bookId);
  }
  var downloadFormat = getQueryParam("download");
  if (downloadFormat) {
    var downloadModals = document.getElementsByTagName("div");
    for (var i = 0; i < downloadModals.length; i++) {
      if (downloadModals[i].id.indexOf("shelf-downloads-") === 0) {
        openModal(downloadModals[i].id);
        break;
      }
    }
  }
};
