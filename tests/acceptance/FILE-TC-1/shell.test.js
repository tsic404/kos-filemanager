// FILE-TC-1 from the window side: the window renders the authorized volume list
// the data plane serves, opens in the stored list or grid view, and starts at
// the stored initial path. The window neither reads nor writes a preference
// itself, so a reference to another account's volume never reaches the list
// (FILE-AC-1, FILE-C-1, DASH-C-27).

import assert from "node:assert/strict";
import test from "node:test";

import { createFileManager } from "../../../pkg/product/assets/app.js";
import {
  fakeDocument,
  fakeElement,
  fakeFetch,
  fakeIntervals,
  fakeWindow,
  settle,
} from "../../shell/harness.js";

const consoleOrigin = "https://console.example.test";

// frameAddress is the address the console builds: the console origin and the
// window instance travel as query values beside the signed asset path.
function frameAddress(query = "") {
  return "https://files.example.test/app-assets/kos-filemanager/kos-filemanager/1.0.0/sha256:abc/assets/index.html?" + query;
}

// init is the console's answer to `ready`: one string token and the theme
// (KOSD-C-75).
function init(token) {
  return { type: "init", token, theme: "system", capabilities: [], expiresAt: "2026-11-12T15:00:00Z" };
}

// aliceData is the one volume the data plane serves to alice.
const aliceData = {
  namespace: "default",
  name: "alice-data",
  owner: "alice",
  phase: "Bound",
  health: "Healthy",
  canBrowse: true,
};

// start mounts the window against one data-plane answer.
function start(answer) {
  const document = fakeDocument({
    status: fakeElement("p"),
    path: fakeElement("span"),
    volumes: fakeElement("ul"),
    "volumes-empty": fakeElement("p"),
    count: fakeElement("span"),
    problems: fakeElement("p"),
    "view-list": fakeElement("button"),
    "view-grid": fakeElement("button"),
  });
  const window = fakeWindow(frameAddress("kos-console=" + encodeURIComponent(consoleOrigin) + "&kos-window=w-1"));
  const network = fakeFetch({
    "GET /files/window": { status: 200, body: answer },
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

test("窗口按授权的卷渲染，并采用存储的网格视图与初始路径", async () => {
  const { document, window, network } = start({
    view: "grid",
    initialPath: "/documents",
    volumes: [aliceData],
  });

  // The handshake opens on the console origin the frame address names.
  assert.deepEqual(window.posted[0], {
    data: { type: "ready", window: "w-1" },
    targetOrigin: consoleOrigin,
  });

  // A message from another origin, or from a source that is not the embedding
  // console, is ignored and reads nothing.
  window.deliver({ origin: "https://elsewhere.example.test", source: window.parent, data: init("forged") });
  window.deliver({ origin: consoleOrigin, source: {}, data: init("forged") });
  assert.equal(network.calls.length, 0);

  window.deliver({ origin: consoleOrigin, source: window.parent, data: init("window-token") });
  await settle();

  // The window reads the volume list from its own data plane with the window
  // token and without the console cookie.
  assert.equal(network.calls.length, 1);
  assert.equal(network.calls[0].url, "/files/window");
  assert.equal(network.calls[0].headers.Authorization, "Bearer window-token");
  assert.equal(network.calls[0].credentials, "omit");

  // The grid view and the initial path are the ones the account stored.
  const volumes = document.getElementById("volumes");
  assert.match(volumes.className, /grid/);
  assert.equal(document.getElementById("view-grid").getAttribute("aria-pressed"), "true");
  assert.equal(document.getElementById("path").textContent, "/documents");

  // Only the volumes the data plane served are rendered; the window never
  // touches the preference API, so an exposed-volume reference cannot become a
  // row (the data plane excludes it).
  assert.match(volumes.textContent, /alice-data/);
  assert.equal(volumes.children.length, 1);
  assert.equal(document.getElementById("volumes-empty").hidden, true);
  assert.equal(document.getElementById("count").textContent, "1 个授权卷");
  assert.ok(
    network.calls.every((call) => !call.url.includes("/preferences")),
    "the window must not read or write preferences itself",
  );

  // The token stays in the closure: it is neither posted nor rendered.
  assert.ok(!JSON.stringify(window.posted).includes("window-token"));
  assert.doesNotMatch(document.getElementById("status").textContent, /window-token/);

  // Switching the view is a local presentation change: no second read and no
  // preference write.
  document.getElementById("view-list").click();
  assert.match(volumes.className, /list/);
  assert.doesNotMatch(volumes.className, /grid/);
  assert.equal(document.getElementById("view-list").getAttribute("aria-pressed"), "true");
  assert.equal(network.calls.length, 1);
});

test("无授权卷时给出空状态，未知视图回退到列表，空初始路径落在卷根", async () => {
  const { document, window, network } = start({
    view: "carousel",
    initialPath: "",
    volumes: [],
  });
  window.deliver({ origin: consoleOrigin, source: window.parent, data: init("window-token") });
  await settle();

  assert.equal(network.calls.length, 1);
  const volumes = document.getElementById("volumes");
  assert.match(volumes.className, /list/);
  assert.equal(document.getElementById("view-list").getAttribute("aria-pressed"), "true");
  assert.equal(document.getElementById("path").textContent, "卷根");
  assert.equal(document.getElementById("volumes-empty").hidden, false);
  assert.equal(document.getElementById("count").textContent, "0 个授权卷");
});
