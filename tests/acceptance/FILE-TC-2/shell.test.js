// FILE-TC-2 from the window side: the window opens its handshake only on the
// console origin the frame address names, accepts one init from that origin and
// source, and reads the data plane with the delivered application token alone —
// never with the console session cookie (FILE-AC-1, FILE-C-1, KOSD-C-75).

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

function frameAddress(query = "") {
  return "https://files.example.test/app-assets/kos-filemanager/kos-filemanager/1.0.0/sha256:abc/assets/index.html?" + query;
}

function init(token) {
  return { type: "init", token, theme: "system", capabilities: [], expiresAt: "2026-11-12T15:00:00Z" };
}

const windowAnswer = { view: "list", initialPath: "", volumes: [] };

function start({ query, answer = windowAnswer } = {}) {
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
  const window = fakeWindow(frameAddress(query));
  const network = fakeFetch({
    "GET /files/window": { status: answer.status || 200, body: answer.body || answer },
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

test("窗口地址缺少控制台来源时不打招呼，也不读取任何东西", () => {
  const { document, window, network } = start({ query: "kos-window=w-1" });
  assert.equal(window.posted.length, 0);
  assert.equal(network.calls.length, 0);
  assert.match(document.getElementById("status").textContent, /不能启动/);
});

test("握手只认控制台来源与父窗口，数据面只用应用令牌", async () => {
  const { document, window, network } = start({
    query: "kos-console=" + encodeURIComponent(consoleOrigin) + "&kos-window=w-1",
  });
  assert.deepEqual(window.posted[0], {
    data: { type: "ready", window: "w-1" },
    targetOrigin: consoleOrigin,
  });

  // Another origin, another source, and an unknown message are all ignored.
  window.deliver({ origin: "https://elsewhere.example.test", source: window.parent, data: init("forged") });
  window.deliver({ origin: consoleOrigin, source: {}, data: init("forged") });
  window.deliver({ origin: consoleOrigin, source: window.parent, data: { type: "ping" } });
  assert.equal(network.calls.length, 0);

  window.deliver({ origin: consoleOrigin, source: window.parent, data: init("window-token") });
  await settle();

  // The read carries the application token and no console cookie; the token is
  // not a cookie value the browser would send on its own.
  assert.equal(network.calls.length, 1);
  assert.equal(network.calls[0].headers.Authorization, "Bearer window-token");
  assert.equal(network.calls[0].credentials, "omit");
  assert.equal(network.calls[0].headers.Cookie, undefined);

  // A second init does not replace the delivered token or start another read.
  window.deliver({ origin: consoleOrigin, source: window.parent, data: init("second-token") });
  await settle();
  assert.equal(network.calls.length, 1);
  assert.equal(network.calls[0].headers.Authorization, "Bearer window-token");

  // A running window reports liveness inside the console's silence window and
  // never posts the token back.
  const alive = document.getElementById("status");
  assert.ok(alive.textContent.length > 0);
  assert.ok(!JSON.stringify(window.posted).includes("window-token"));
});

test("失效令牌让窗口要求重开，而不是用控制台会话继续读取", async () => {
  const { document, window, network } = start({
    query: "kos-console=" + encodeURIComponent(consoleOrigin) + "&kos-window=w-1",
    answer: { status: 401, body: { error: { code: "token_invalid", message: "应用令牌已失效，请重新打开窗口" } } },
  });
  window.deliver({ origin: consoleOrigin, source: window.parent, data: init("window-token") });
  await settle();

  assert.equal(network.calls.length, 1);
  assert.match(document.getElementById("status").textContent, /重新打开窗口/);
  assert.equal(document.getElementById("volumes").children.length, 0);
  assert.equal(document.getElementById("volumes-empty").hidden, false);
});
