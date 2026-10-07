import { mkdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

import { loadCaptureSpec } from "./capture-compat.mjs";

const directory = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(process.env.SCREENSHOT_ROOT || path.resolve(directory, "../.."));
const baseURL = process.env.SCREENSHOT_BASE_URL || "http://127.0.0.1:8080";
const output = process.env.SCREENSHOT_OUTPUT || path.join(root, "docs/assets/images/generated");
const username = "admin";
const password = "password";
const buildTag = process.env.SCREENSHOT_BUILD_TAG;
const includeScenarios = new Set((process.env.SCREENSHOT_SCENARIOS || "").split(",").filter(Boolean));
const excludeScenarios = new Set((process.env.SCREENSHOT_EXCLUDE_SCENARIOS || "").split(",").filter(Boolean));
const { scenarios: loadedScenarios } = await loadCaptureSpec(root, buildTag);
const scenarios = loadedScenarios.filter((scenario) =>
  (includeScenarios.size === 0 || includeScenarios.has(scenario.id)) && !excludeScenarios.has(scenario.id),
);

const viewports = [
  { name: "desktop", width: 1440, height: 810 },
  { name: "ereader", width: 768, height: 1024 },
];

if (!Array.isArray(scenarios)) throw new Error("Screenshot catalog must be a YAML list");
for (const scenario of scenarios) {
  if (!scenario?.id || !Array.isArray(scenario.actions)) throw new Error("Each screenshot requires an id and actions list");
  for (const annotation of scenario.annotations || []) {
    if (!annotation.selector || !annotation.label) throw new Error(`Screenshot ${scenario.id} has an invalid annotation`);
  }
}

await mkdir(output, { recursive: true });

async function login(page) {
  await page.goto(`${baseURL}/login`, { waitUntil: "networkidle" });
  await page.locator('input[name="username"]').fill(username);
  await page.locator('input[name="password"]').fill(password);
  await Promise.all([page.waitForURL(`${baseURL}/`), page.locator('button[type="submit"]').click()]);
  if (buildTag) await page.locator(".footer", { hasText: buildTag }).waitFor();
}

function actionEntry(action, scenarioID) {
  if (typeof action === "string") return [action, undefined];
  if (!action || typeof action !== "object" || Array.isArray(action) || Object.keys(action).length !== 1) {
    throw new Error(`Screenshot ${scenarioID} has an invalid action`);
  }
  return Object.entries(action)[0];
}

function resolve(value, context, scenarioID) {
  if (typeof value !== "string") throw new Error(`Screenshot ${scenarioID} action value must be text`);
  return value.replace(/\{\{(\w+)\}\}/g, (_, key) => {
    if (!(key in context)) throw new Error(`Screenshot ${scenarioID} references unknown value ${key}`);
    return context[key];
  });
}

async function openModal(page, modalID) {
  await page.evaluate((id) => window.openModal(id), modalID);
  await page.locator(`#${modalID}:visible`).waitFor();
}

async function runAction(page, context, action, scenarioID) {
  const [name, value] = actionEntry(action, scenarioID);
  switch (name) {
    case "login":
      if (value !== undefined) throw new Error(`Screenshot ${scenarioID} login does not accept a value`);
      await login(page);
      return;
    case "configure_smtp":
      if (value !== undefined) throw new Error(`Screenshot ${scenarioID} configure_smtp does not accept a value`);
      await page.goto(`${baseURL}/admin/smtp`, { waitUntil: "networkidle" });
      await page.locator('input[name="host"]').fill("smtp.example.com");
      await page.locator('input[name="from_addr"]').fill("library@example.com");
      await Promise.all([
        page.waitForURL(`${baseURL}/admin/smtp`),
        page.locator('form[action="/admin/settings/smtp"] button[type="submit"]').click(),
      ]);
      return;
    case "goto":
      await page.goto(`${baseURL}${resolve(value, context, scenarioID)}`, { waitUntil: "networkidle" });
      return;
    case "set_theme":
      await page.context().addCookies([{ name: "sideload-library-theme", value: resolve(value, context, scenarioID), url: baseURL }]);
      return;
    case "open_book": {
      const title = resolve(value, context, scenarioID);
      await page.goto(`${baseURL}/?q=${encodeURIComponent(title)}`, { waitUntil: "networkidle" });
      const card = page.locator(".card", { hasText: title }).first();
      const cardID = await card.getAttribute("id");
      if (!cardID) throw new Error(`Screenshot ${scenarioID} could not find book ${title}`);
      context.bookID = cardID.replace("card-", "");
      await openModal(page, `book-${context.bookID}`);
      return;
    }
    case "toggle_shelf": {
      const shelf = resolve(value, context, scenarioID);
      const button = page.locator(".modal-overlay:visible button.shelf-quick-action, .modal-overlay:visible button.shelf-toggle", { hasText: shelf }).first();
      const wasOn = await button.evaluate((element) => element.classList.contains("is-on"));
      await button.click();
      await button.evaluate((element, state) => new Promise((resolve, reject) => {
        const deadline = Date.now() + 10_000;
        const waitForStateChange = () => {
          if (element.classList.contains("is-on") !== state.wasOn) {
            resolve();
            return;
          }
          if (Date.now() >= deadline) {
            reject(new Error(`Shelf state did not change for ${state.scenarioID}: ${state.shelf}`));
            return;
          }
          window.setTimeout(waitForStateChange, 50);
        };
        waitForStateChange();
      }), { wasOn, scenarioID, shelf });
      return;
    }
    case "create_shelf": {
      const name = resolve(value, context, scenarioID);
      await page.goto(`${baseURL}/account/shelves`, { waitUntil: "networkidle" });
      await page.locator('input[name="name"]').fill(name);
      await Promise.all([
        page.waitForURL(`${baseURL}/account/shelves`),
        page.locator('form[action="/account/shelves"] button[type="submit"]').click(),
      ]);
      const href = await page.locator(".account-shelves a", { hasText: name }).first().getAttribute("href");
      const match = href && href.match(/^\/shelves\/(\d+)$/);
      if (!match) throw new Error(`Screenshot ${scenarioID} could not find created shelf ${name}`);
      context.shelfID = match[1];
      return;
    }
    case "open_shelf_picker": {
      if (value !== undefined) throw new Error(`Screenshot ${scenarioID} open_shelf_picker does not accept a value`);
      await page.locator(".modal-overlay:visible .shelf-quick-more").click();
      await page.locator(".modal-overlay:visible .shelf-picker-list").waitFor();
      return;
    }
    case "back_to_book": {
      if (value !== undefined) throw new Error(`Screenshot ${scenarioID} back_to_book does not accept a value`);
      await page.locator(".modal-overlay:visible .modal-back-button").click();
      await page.locator(".modal-overlay:visible .shelf-quick-actions").waitFor();
      return;
    }
    case "open_modal":
      await openModal(page, resolve(value, context, scenarioID));
      return;
    case "open_first_modal": {
      const modalID = await page.locator(resolve(value, context, scenarioID)).first().getAttribute("id");
      if (!modalID) throw new Error(`Screenshot ${scenarioID} could not find a modal`);
      await openModal(page, modalID);
      return;
    }
    default:
      throw new Error(`Screenshot ${scenarioID} uses unknown action ${name}`);
  }
}

async function prepareScenario(page, scenario) {
  const context = {};
  for (const action of scenario.actions) await runAction(page, context, action, scenario.id);
}

async function applyAnnotations(page, annotations = []) {
  const targets = [];
  for (const annotation of annotations) {
    const target = page.locator(annotation.selector).first();
    await target.waitFor();
    const box = await target.boundingBox();
    if (!box) throw new Error(`Annotation target is not visible: ${annotation.selector}`);
    targets.push({ ...annotation, box });
  }

  if (!targets.length) return;
  await page.evaluate((items) => {
    const viewportPadding = 8;
    const gap = 8;
    const surrounds = items.map((item) => {
      const surround = 8;
      const left = Math.max(0, item.box.x - surround);
      const top = Math.max(0, item.box.y - surround);
      const right = Math.min(window.innerWidth, item.box.x + item.box.width + surround);
      const bottom = Math.min(window.innerHeight, item.box.y + item.box.height + surround);
      return { left, top, right, bottom };
    });
    const overlaps = (first, second) => first.left < second.right && first.right > second.left && first.top < second.bottom && first.bottom > second.top;
    const clamp = (value, min, max) => Math.min(Math.max(min, value), max);
    const occupied = [...surrounds];

    for (const [index, item] of items.entries()) {
      const { left, top, right, bottom } = surrounds[index];
      const highlight = document.createElement("div");
      highlight.setAttribute("data-screenshot-annotation", "highlight");
      Object.assign(highlight.style, {
        position: "fixed", zIndex: "2147483646", pointerEvents: "none", boxSizing: "border-box",
        left: `${left}px`, top: `${top}px`, width: `${Math.max(0, right - left)}px`, height: `${Math.max(0, bottom - top)}px`,
        border: "3px solid #ef4444", borderRadius: "8px", background: "transparent",
      });
      document.body.append(highlight);

      const label = document.createElement("div");
      label.setAttribute("data-screenshot-annotation", "label");
      label.textContent = item.label;
      Object.assign(label.style, {
        position: "fixed", zIndex: "2147483647", pointerEvents: "none", boxSizing: "border-box",
        maxWidth: "min(280px, calc(100vw - 16px))", padding: "6px 9px", border: "none", borderRadius: "4px",
        background: "rgba(0, 0, 0, 0.78)", color: "#ef4444", font: "600 14px/1.2 system-ui, sans-serif", visibility: "hidden",
      });
      document.body.append(label);

      const width = label.offsetWidth;
      const height = label.offsetHeight;
      const candidate = (x, y) => ({
        left: clamp(x, viewportPadding, window.innerWidth - width - viewportPadding),
        top: clamp(y, viewportPadding, window.innerHeight - height - viewportPadding),
        right: 0, bottom: 0,
      });
      const above = candidate(left, top - height - gap);
      const below = candidate(left, bottom + gap);
      const preferred = item.placement === "bottom" ? [below, above] : [above, below];
      const options = [
        ...preferred, candidate(right + gap, top), candidate(left - width - gap, top),
        candidate(right - width, top - height - gap), candidate(right - width, bottom + gap),
      ].map((position) => ({ ...position, right: position.left + width, bottom: position.top + height }));
      let position = options.find((option) => !occupied.some((area) => overlaps(option, area)));

      if (!position) {
        for (let y = viewportPadding; y <= window.innerHeight - height - viewportPadding && !position; y += gap) {
          for (let x = viewportPadding; x <= window.innerWidth - width - viewportPadding; x += gap) {
            const option = { left: x, top: y, right: x + width, bottom: y + height };
            if (!occupied.some((area) => overlaps(option, area))) {
              position = option;
              break;
            }
          }
        }
      }

      if (!position) throw new Error(`No clear label position for annotation: ${item.label}`);
      label.style.left = `${position.left}px`;
      label.style.top = `${position.top}px`;
      label.style.visibility = "visible";
      occupied.push(position);
    }
  }, targets);
}

const browser = await chromium.launch({ headless: true });
try {
  for (const viewport of viewports) {
    for (const scenario of scenarios) {
      const page = await browser.newPage({ viewport: { width: viewport.width, height: viewport.height }, deviceScaleFactor: 1 });
      await page.emulateMedia({ colorScheme: "light", reducedMotion: "reduce" });
      await prepareScenario(page, scenario);
      await page.evaluate(() => document.fonts.ready);
      await applyAnnotations(page, scenario.annotations);
      await page.screenshot({ path: path.join(output, `${scenario.id}-${viewport.name}.png`), type: "png" });
      await page.close();
    }
  }
} finally {
  await browser.close();
}
