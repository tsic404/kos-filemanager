// FILE-TC-5 from the window side: a text and an image preview render the content
// the data plane served, the window issues nothing but reads, and a type the
// data plane cannot render answers a readable reason instead of a blank panel or
// a download (FILE-AC-3, FILE-C-3).

import assert from "node:assert/strict";
import test from "node:test";

import { createFileManager } from "../../../pkg/product/assets/app.js";
import {
  fakeFetch,
  fakeIntervals,
  fakeWindow,
  findByClass,
  settle,
  windowDocument,
} from "../../shell/harness.js";

const consoleOrigin = "https://console.example.test";

const volume = {
  namespace: "default",
  name: "preview-data",
  owner: "alice",
  phase: "Bound",
  health: "Healthy",
  canBrowse: true,
};

const modified = "2026-04-03T10:15:00Z";

function frameAddress(query = "") {
  return "https://files.example.test/app-assets/kos-filemanager/kos-filemanager/1.0.0/sha256:abc/assets/index.html?" + query;
}

function init(token) {
  return { type: "init", token, theme: "system", capabilities: [], expiresAt: "2026-11-12T15:00:00Z" };
}

function entry(name, kind, size) {
  return { name, kind, size, modifiedAt: modified };
}

function preview(path, extra) {
  return {
    volume: "default/preview-data",
    path,
    kind: "text",
    contentType: "text/plain; charset=utf-8",
    size: 24,
    modifiedAt: modified,
    observedAt: "2026-04-03T10:16:00Z",
    content: "",
    truncated: false,
    ...extra,
  };
}

// start mounts the window against one data-plane answer per route.
function start({ routes }) {
  const document = windowDocument();
  const window = fakeWindow(frameAddress("kos-console=" + encodeURIComponent(consoleOrigin) + "&kos-window=w-1"));
  const network = fakeFetch({
    "GET /files/window": { status: 200, body: { view: "list", initialPath: "", volumes: [volume] } },
    ...routes,
  });
  const clock = fakeIntervals();
  createFileManager({
    document,
    window,
    fetch: network.fetch,
    setInterval: clock.setInterval,
  }).start();
  return { document, window, network, clock };
}

// openVolume delivers the console's init and opens the volume the window lists.
async function openVolume(document, window, network) {
  window.deliver({ origin: consoleOrigin, source: window.parent, data: init("window-token") });
  await settle();
  openButton(document).click();
  await settle();
  return network.calls.length;
}

// openButton is the control one volume renders to open it for browsing.
function openButton(document) {
  const button = findByClass(document.getElementById("volumes"), "volume-open");
  assert.ok(button, "the volume list offers a way to open a volume");
  return button;
}

// entryButton finds the control one entry name renders.
function entryButton(document, name) {
  for (const item of document.getElementById("entries").children) {
    const button = findByClass(item, "entry-name");
    if (button && button.dataset.entry === name) return button;
  }
  throw new Error(`no entry named ${name}`);
}

test("文本预览显示内容与元数据，且窗口只发出读取请求", async () => {
  const text = "第一行：预览不应改动文件\nsecond line\n";
  const { document, window, network } = start({
    routes: {
      "GET /files/volumes/default/preview-data/entries": {
        status: 200,
        body: {
          path: "",
          limit: 500,
          total: 1,
          truncated: false,
          entries: [entry("notes.txt", "file", text.length)],
        },
      },
      "GET /files/volumes/default/preview-data/preview?path=notes.txt": {
        status: 200,
        body: preview("notes.txt", { content: text, size: text.length }),
      },
    },
  });
  await openVolume(document, window, network);

  entryButton(document, "notes.txt").click();
  await settle();

  // The preview is one read of one path, carrying the window token and no
  // console cookie.
  const read = network.calls[network.calls.length - 1];
  assert.equal(read.method, "GET");
  assert.equal(read.url, "/files/volumes/default/preview-data/preview?path=notes.txt");
  assert.equal(read.headers.Authorization, "Bearer window-token");
  assert.equal(read.credentials, "omit");

  // The text is shown verbatim, with the type, size and modification time that
  // came with it.
  const panel = document.getElementById("preview-panel");
  const body = document.getElementById("preview-text");
  assert.equal(panel.hidden, false);
  assert.equal(body.hidden, false);
  assert.equal(body.textContent, text);
  assert.equal(document.getElementById("preview-image").hidden, true);
  const meta = document.getElementById("preview-meta").textContent;
  for (const part of ["notes.txt", "text/plain", `${text.length} B`, "2026-04-03 10:15 UTC"]) {
    assert.ok(meta.includes(part), `preview metadata shows ${part}: ${meta}`);
  }
  assert.equal(document.getElementById("preview-cut").hidden, true);

  // The directory stays readable behind the preview, and every request this
  // window sent is a read: nothing on the browse or preview surface writes.
  assert.equal(document.getElementById("entries").children.length, 1);
  assert.ok(network.calls.every((call) => call.method === "GET"), "the window only reads");

  document.getElementById("preview-close").click();
  assert.equal(panel.hidden, true);
});

test("图片预览以 data URL 显示", async () => {
  const dataURL =
    "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAFElEQVR4nGP8z8Dwn4GBgYGJAQoAHgQCAf2XwS8AAAAASUVORK5CYII=";
  const { document, window, network } = start({
    routes: {
      "GET /files/volumes/default/preview-data/entries": {
        status: 200,
        body: {
          path: "",
          limit: 500,
          total: 1,
          truncated: false,
          entries: [entry("photo.png", "file", 95)],
        },
      },
      "GET /files/volumes/default/preview-data/preview?path=photo.png": {
        status: 200,
        body: preview("photo.png", { kind: "image", contentType: "image/png", size: 95, content: dataURL }),
      },
    },
  });
  await openVolume(document, window, network);

  entryButton(document, "photo.png").click();
  await settle();

  // The image is rendered from the data URL the data plane served, not fetched
  // from a second address and not offered as a download.
  const image = document.getElementById("preview-image");
  assert.equal(document.getElementById("preview-panel").hidden, false);
  assert.equal(image.hidden, false);
  assert.equal(image.getAttribute("src"), dataURL);
  assert.equal(document.getElementById("preview-text").hidden, true);
  assert.match(document.getElementById("preview-meta").textContent, /image\/png/);
});

test("不支持预览的类型给出原因，不出现空白页或下载", async () => {
  const { document, window, network } = start({
    routes: {
      "GET /files/volumes/default/preview-data/entries": {
        status: 200,
        body: {
          path: "",
          limit: 500,
          total: 1,
          truncated: false,
          entries: [entry("archive.zip", "file", 37)],
        },
      },
      "GET /files/volumes/default/preview-data/preview?path=archive.zip": {
        status: 415,
        body: {
          error: {
            code: "preview_unsupported",
            message: "不支持预览 archive.zip（.zip）：仅支持文本与图片",
          },
        },
      },
    },
  });
  await openVolume(document, window, network);

  entryButton(document, "archive.zip").click();
  await settle();

  // The reason the data plane gave is readable, and the refusal is reported as
  // one.
  const status = document.getElementById("status");
  assert.ok(status.textContent.includes(".zip"), `reason names the type: ${status.textContent}`);
  assert.ok(status.textContent.includes("仅支持文本与图片"), `reason names the support: ${status.textContent}`);
  assert.ok(status.classList.contains("blocked"));

  // No blank preview and no download: the preview surface stays closed and no
  // content is rendered.
  assert.equal(document.getElementById("preview-panel").hidden, true);
  assert.equal(document.getElementById("preview-text").hidden, true);
  assert.equal(document.getElementById("preview-image").hidden, true);
  assert.equal(document.getElementById("preview-image").getAttribute("src"), null);
  assert.equal(document.getElementById("entries").children.length, 1);
});
