// FILE-TC-4 from the window side: the window opens the first volume it is given
// at the stored initial path, renders each entry's name, type, size and
// modification time in both views, walks into a directory and back to the volume
// root, marks a truncated listing, and reads the reason a refused browse or an
// out-of-volume link answers (FILE-AC-3, FILE-C-3, DASH-C-27).

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

// volume is one authorized volume the data plane serves to this account.
const volume = {
  namespace: "default",
  name: "alice-data",
  owner: "alice",
  phase: "Bound",
  health: "Healthy",
  canBrowse: true,
};

// modified is the instant every entry carries, so the rendered time is compared
// with one exact label.
const modified = "2026-04-02T09:30:00Z";

function frameAddress(query = "") {
  return "https://files.example.test/app-assets/kos-filemanager/kos-filemanager/1.0.0/sha256:abc/assets/index.html?" + query;
}

function init(token) {
  return { type: "init", token, theme: "system", capabilities: [], expiresAt: "2026-11-12T15:00:00Z" };
}

function entry(name, kind, size) {
  return { name, kind, size, modifiedAt: modified };
}

// listing is one browse answer the data plane would serve.
function listing(path, entries, extra = {}) {
  return {
    volume: "default/alice-data",
    path,
    observedAt: "2026-04-02T09:31:00Z",
    limit: 500,
    total: entries.length,
    truncated: false,
    entries,
    ...extra,
  };
}

// start mounts the window against one data-plane answer per route.
function start({ answer, routes }) {
  const document = windowDocument();
  const window = fakeWindow(frameAddress("kos-console=" + encodeURIComponent(consoleOrigin) + "&kos-window=w-1"));
  const network = fakeFetch({ "GET /files/window": { status: 200, body: answer }, ...routes });
  const clock = fakeIntervals();
  createFileManager({
    document,
    window,
    fetch: network.fetch,
    setInterval: clock.setInterval,
  }).start();
  return { document, window, network, clock };
}

// openWindow delivers the console's init and lets the window read its volumes.
async function openWindow(document, window, network) {
  window.deliver({ origin: consoleOrigin, source: window.parent, data: init("window-token") });
  await settle();
  return network.calls.length;
}

// openButton is the control one volume renders to open it for browsing.
function openButton(document) {
  const button = findByClass(document.getElementById("volumes"), "volume-open");
  assert.ok(button, "the volume list offers a way to open a volume");
  return button;
}

// entryButton finds the control one entry name renders, so a case clicks the
// entry the listing shows instead of a position in the list.
function entryButton(document, name) {
  for (const item of document.getElementById("entries").children) {
    const button = findByClass(item, "entry-name");
    if (button && button.dataset.entry === name) return button;
  }
  throw new Error(`no entry named ${name}`);
}

test("窗口在第一个卷里按存储的初始路径浏览，两种视图都渲染条目字段", async () => {
  const { document, window, network } = start({
    answer: { view: "grid", initialPath: "/documents", volumes: [volume] },
    routes: {
      "GET /files/volumes/default/alice-data/entries?path=documents": {
        status: 200,
        body: listing("documents", [entry("docs", "directory", 0), entry("notes.txt", "file", 12)]),
      },
    },
  });
  await openWindow(document, window, network);

  // The window starts on the authorized volume list; nothing is browsed until
  // one volume is opened.
  assert.equal(network.calls.length, 1);
  assert.equal(document.getElementById("browser-panel").hidden, true);
  assert.equal(document.getElementById("path").textContent, "/documents");

  openButton(document).click();
  await settle();

  // The stored initial path is where the first browse lands: the preference
  // reads "/documents" while the data plane addresses the relative "documents".
  assert.equal(network.calls.length, 2);
  assert.equal(network.calls[1].url, "/files/volumes/default/alice-data/entries?path=documents");
  assert.equal(network.calls[1].method, "GET");
  assert.equal(network.calls[1].headers.Authorization, "Bearer window-token");
  assert.equal(network.calls[1].credentials, "omit");
  assert.equal(document.getElementById("browser-panel").hidden, false);
  assert.equal(document.getElementById("path").textContent, "/documents");

  // Every entry carries the four fields the contract names.
  const entries = document.getElementById("entries");
  assert.equal(entries.children.length, 2);
  assert.match(entries.className, /grid/);
  const rendered = entries.textContent;
  for (const part of ["docs", "目录", "notes.txt", "文件", "12 B", "2026-04-02 09:30 UTC"]) {
    assert.ok(rendered.includes(part), `entry surface shows ${part}: ${rendered}`);
  }
  assert.equal(document.getElementById("entries-count").textContent, "2 项");
  assert.equal(document.getElementById("truncated").hidden, true);

  // The view switch is one presentation choice for the volume list and the
  // directory alike, and it reads nothing.
  document.getElementById("view-list").click();
  assert.match(entries.className, /list/);
  assert.doesNotMatch(entries.className, /grid/);
  assert.match(document.getElementById("volumes").className, /list/);
  assert.equal(document.getElementById("view-list").getAttribute("aria-pressed"), "true");
  assert.equal(network.calls.length, 2);
});

test("进入目录、返回卷根与刷新都重新读取卷的当前内容", async () => {
  const root = {
    status: 200,
    body: listing("", [entry("docs", "directory", 0), entry("alpha.txt", "file", 5)]),
  };
  const { document, window, network } = start({
    answer: { view: "list", initialPath: "", volumes: [volume] },
    routes: {
      "GET /files/volumes/default/alice-data/entries": root,
      "GET /files/volumes/default/alice-data/entries?path=docs": {
        status: 200,
        body: listing("docs", [entry("inner.txt", "file", 3)]),
      },
    },
  });
  await openWindow(document, window, network);

  openButton(document).click();
  await settle();

  // An empty initial path lands on the volume root, without a query.
  assert.equal(network.calls.length, 2);
  assert.equal(network.calls[1].url, "/files/volumes/default/alice-data/entries");
  assert.equal(document.getElementById("path").textContent, "卷根");
  assert.match(document.getElementById("entries").textContent, /alpha\.txt/);

  // Entering a directory reads that path and moves the location bar.
  entryButton(document, "docs").click();
  await settle();
  assert.equal(network.calls[2].url, "/files/volumes/default/alice-data/entries?path=docs");
  assert.equal(document.getElementById("path").textContent, "/docs");
  assert.match(document.getElementById("entries").textContent, /inner\.txt/);

  // Returning to the volume root reads the root again.
  document.getElementById("root").click();
  await settle();
  assert.equal(network.calls[3].url, "/files/volumes/default/alice-data/entries");
  assert.equal(document.getElementById("path").textContent, "卷根");
  assert.match(document.getElementById("entries").textContent, /alpha\.txt/);

  // Another window deletes alpha.txt and creates beta.txt between the two
  // browses; the next browse of this window shows the change.
  root.body = listing("", [entry("beta.txt", "file", 6), entry("docs", "directory", 0)]);
  document.getElementById("refresh").click();
  await settle();
  assert.equal(network.calls.length, 5);
  const rendered = document.getElementById("entries").textContent;
  assert.match(rendered, /beta\.txt/);
  assert.doesNotMatch(rendered, /alpha\.txt/);
});

test("超限目录列表被截断并标注", async () => {
  const { document, window, network } = start({
    answer: { view: "list", initialPath: "", volumes: [volume] },
    routes: {
      "GET /files/volumes/default/alice-data/entries": {
        status: 200,
        body: listing("", [entry("a.txt", "file", 1), entry("b.txt", "file", 1)], {
          limit: 2,
          total: 5,
          truncated: true,
        }),
      },
    },
  });
  await openWindow(document, window, network);

  openButton(document).click();
  await settle();

  // The bound is visible: the window says what it shows of how many.
  const note = document.getElementById("truncated");
  assert.equal(note.hidden, false);
  assert.equal(note.textContent, "已截断：显示前 2 项，共 5 项");
  assert.equal(document.getElementById("entries").children.length, 2);
  assert.equal(document.getElementById("entries-count").textContent, "2 项");
});

test("越界链接与不可用路径的拒绝原因可读，且不离开当前目录", async () => {
  const refuse = (status, code, message) => ({ status, body: { error: { code, message } } });
  const { document, window, network } = start({
    answer: { view: "list", initialPath: "", volumes: [volume] },
    routes: {
      "GET /files/volumes/default/alice-data/entries": {
        status: 200,
        body: listing("", [
          entry("bad", "directory", 0),
          entry("missing", "directory", 0),
          entry("unavailable", "directory", 0),
          entry("leak-link.txt", "symlink", 0),
        ]),
      },
      "GET /files/volumes/default/alice-data/entries?path=bad": refuse(
        400,
        "path_invalid",
        "路径无效：不能是绝对路径或包含上级目录",
      ),
      "GET /files/volumes/default/alice-data/entries?path=missing": refuse(
        404,
        "path_not_found",
        "路径 missing 不存在",
      ),
      "GET /files/volumes/default/alice-data/entries?path=unavailable": refuse(
        503,
        "files_unavailable",
        "卷 default/alice-data 的数据面目录未在本服务挂载",
      ),
      "GET /files/volumes/default/alice-data/preview?path=leak-link.txt": refuse(
        403,
        "path_outside_volume",
        "路径 leak-link.txt 越出卷根：指向卷外的符号链接不被跟随",
      ),
    },
  });
  await openWindow(document, window, network);

  openButton(document).click();
  await settle();

  const refusals = [
    ["bad", "路径无效"],
    ["missing", "路径 missing 不存在"],
    ["unavailable", "数据面目录未在本服务挂载"],
    ["leak-link.txt", "越出卷根"],
  ];
  for (const [name, reason] of refusals) {
    entryButton(document, name).click();
    await settle();
    const status = document.getElementById("status");
    assert.ok(status.textContent.includes(reason), `${name} reason: ${status.textContent}`);
    assert.ok(status.classList.contains("blocked"), `${name} is reported as a refusal`);
    // A refused request never moves the location, never replaces the listing
    // and never shows a preview of the refused path.
    assert.equal(document.getElementById("path").textContent, "卷根");
    assert.equal(document.getElementById("entries").children.length, 4);
    assert.equal(document.getElementById("preview-panel").hidden, true);
  }

  // No refusal carried content from outside the volume into the document.
  assert.doesNotMatch(document.getElementById("entries").textContent, /leak-content/);
});
