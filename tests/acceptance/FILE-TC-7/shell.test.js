// FILE-TC-7 from the window side: every entry offers rename, copy, move and
// delete; a name the destination already holds shows the data plane's reason
// and the listing returns to what the volume holds; a delete states what it
// removes before it runs and runs only after the reader confirms; the volume
// root is refused; a failed copy shows the reason with the source still listed
// and no target row invented (FILE-AC-5, FILE-C-5).

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

const modified = "2026-04-03T10:15:00Z";

// primary is the volume the window opens; shelf is the second volume the same
// account owns, so a copy or move may target it.
const primary = {
  namespace: "default",
  name: "primary",
  owner: "alice",
  phase: "Bound",
  health: "Healthy",
  canBrowse: true,
};

const shelf = {
  namespace: "default",
  name: "shelf",
  owner: "alice",
  phase: "Bound",
  health: "Healthy",
  canBrowse: true,
};

function frameAddress(query = "") {
  return "https://files.example.test/app-assets/kos-filemanager/kos-filemanager/1.0.0/sha256:abc/assets/index.html?" + query;
}

function init(token) {
  return { type: "init", token, theme: "system", capabilities: [], expiresAt: "2026-11-12T15:00:00Z" };
}

function entry(name, kind, size) {
  return { name, kind, size, modifiedAt: modified };
}

// listing is one browse answer of the open volume's root.
function listing(entries) {
  return {
    volume: "default/primary",
    path: "",
    observedAt: "2026-04-03T10:16:00Z",
    limit: 500,
    total: entries.length,
    truncated: false,
    entries,
  };
}

// change is the data plane's outcome of one change operation.
function change(answer) {
  return {
    operation: "rename",
    volume: "default/primary",
    observedAt: "2026-04-03T10:17:00Z",
    ...answer,
  };
}

// start mounts the window against one data-plane answer per route.
function start(routes) {
  const document = windowDocument();
  const window = fakeWindow(frameAddress("kos-console=" + encodeURIComponent(consoleOrigin) + "&kos-window=w-1"));
  const network = fakeFetch({
    "GET /files/window": { status: 200, body: { view: "list", initialPath: "", volumes: [primary, shelf] } },
    ...routes,
  });
  const clock = fakeIntervals();
  createFileManager({
    document,
    window,
    fetch: network.fetch,
    setInterval: clock.setInterval,
  }).start();
  return { document, window, network };
}

// openVolume delivers the console's init and opens the volume the window lists.
async function openVolume(document, window) {
  window.deliver({ origin: consoleOrigin, source: window.parent, data: init("window-token") });
  await settle();
  const button = findByClass(document.getElementById("volumes"), "volume-open");
  assert.ok(button, "the volume list offers a way to open a volume");
  button.click();
  await settle();
}

// controls lists every element below root, so a case finds the control one
// entry renders without knowing the row's shape.
function controls(root) {
  const found = [];
  const walk = (node) => {
    if (!node || !Array.isArray(node.children)) return;
    for (const child of node.children) {
      found.push(child);
      walk(child);
    }
  };
  walk(root);
  return found;
}

// changeButton is the control one entry renders for one operation.
function changeButton(document, name, kind) {
  const button = controls(document.getElementById("entries")).find(
    (node) => node.dataset && node.dataset.entry === name && node.dataset.change === kind,
  );
  assert.ok(button, `条目 ${name} 提供 ${kind} 操作`);
  return button;
}

// rowNames are the entry names the listing shows, in row order.
function rowNames(document) {
  return document.getElementById("entries").children.map((item) => {
    const name = findByClass(item, "entry-name");
    return name ? name.textContent : "";
  });
}

// lastSent answers the newest request of one method.
function lastSent(network, method) {
  const sent = network.calls.filter((call) => call.method === method);
  return sent[sent.length - 1];
}

test("四项操作都在条目上，重命名提交路径与新名后列表换成新名", async () => {
  let answer = listing([entry("notes.txt", "file", 12), entry("nested", "directory", 0)]);
  const { document, window, network } = start({
    "GET /files/volumes/default/primary/entries": () => ({ status: 200, body: answer }),
    "POST /files/volumes/default/primary/rename": {
      status: 200,
      body: change({ operation: "rename", path: "notes.txt", targetVolume: "default/primary", targetPath: "final.txt" }),
    },
  });
  await openVolume(document, window);

  // Every entry carries the four operations, named as the reader sees them.
  for (const name of ["notes.txt", "nested"]) {
    const labels = ["rename", "copy", "move", "delete"].map((kind) => changeButton(document, name, kind).textContent);
    assert.deepEqual(labels, ["重命名", "复制", "移动", "删除"]);
  }

  changeButton(document, "notes.txt", "rename").click();
  assert.equal(document.getElementById("change-panel").hidden, false);
  assert.equal(document.getElementById("rename-form").hidden, false);
  assert.equal(document.getElementById("rename-name").value, "notes.txt", "重命名从当前名字开始");

  // The volume holds the new name once the change ran.
  answer = listing([entry("final.txt", "file", 12), entry("nested", "directory", 0)]);
  document.getElementById("rename-name").value = "final.txt";
  document.getElementById("rename-submit").click();
  await settle();

  const sent = lastSent(network, "POST");
  assert.equal(sent.url, "/files/volumes/default/primary/rename");
  assert.deepEqual(JSON.parse(sent.body), { path: "notes.txt", name: "final.txt" });
  assert.equal(sent.headers.Authorization, "Bearer window-token");
  assert.equal(sent.credentials, "omit");

  // The listing is a fresh read of the volume: the new name is there, the old
  // one is gone, and the change surface closes.
  assert.deepEqual(rowNames(document), ["final.txt", "nested"]);
  assert.ok(document.getElementById("status").textContent.includes("final.txt"), "状态报出结果落点");
  assert.equal(document.getElementById("change-panel").hidden, true);
});

test("目标名已存在时显示服务端原因并按卷的真实内容重读列表", async () => {
  const held = [entry("notes.txt", "file", 12), entry("nested", "directory", 0)];
  const { document, window, network } = start({
    "GET /files/volumes/default/primary/entries": { status: 200, body: listing(held) },
    "POST /files/volumes/default/primary/rename": {
      status: 409,
      body: { error: { code: "target_exists", message: "目标 default/primary:nested 已存在，未覆盖" } },
    },
  });
  await openVolume(document, window);

  changeButton(document, "notes.txt", "rename").click();
  document.getElementById("rename-name").value = "nested";
  const before = network.calls.length;
  document.getElementById("rename-submit").click();
  await settle();

  assert.deepEqual(JSON.parse(lastSent(network, "POST").body), { path: "notes.txt", name: "nested" });

  // The reason the data plane gave is readable and names the taken name.
  const status = document.getElementById("status");
  assert.ok(status.textContent.includes("已存在"), `原因可读：${status.textContent}`);
  assert.ok(status.textContent.includes("nested"), `原因点名占用的名字：${status.textContent}`);
  assert.ok(status.classList.contains("blocked"));

  // The list returns to what the volume holds: one row per entry, no replaced
  // row and no half entry; the refresh is a fresh read.
  assert.deepEqual(rowNames(document), ["notes.txt", "nested"]);
  assert.ok(network.calls.length > before + 1, "拒绝后目录被重读");

  // The refused rename stays open with the typed name, so another name can be
  // chosen without starting over.
  assert.equal(document.getElementById("change-panel").hidden, false);
  assert.equal(document.getElementById("rename-name").value, "nested");
});

test("复制的目标卷只来自授权卷列表，成功后源端条目保留", async () => {
  const { document, window, network } = start({
    "GET /files/volumes/default/primary/entries": { status: 200, body: listing([entry("notes.txt", "file", 12)]) },
    "POST /files/volumes/default/primary/copy": {
      status: 200,
      body: change({ operation: "copy", path: "notes.txt", targetVolume: "default/shelf", targetPath: "notes.txt" }),
    },
  });
  await openVolume(document, window);

  changeButton(document, "notes.txt", "copy").click();
  const select = document.getElementById("transfer-volume");

  // The choices are exactly the volumes the data plane listed for this
  // account; the window adds none and judges none of them.
  assert.deepEqual(
    select.children.map((option) => option.value),
    ["default/primary", "default/shelf"],
  );
  assert.equal(select.value, "default/primary", "源卷是默认目标");

  select.value = "default/shelf";
  document.getElementById("transfer-path").value = "notes.txt";
  document.getElementById("transfer-submit").click();
  await settle();

  const sent = lastSent(network, "POST");
  assert.equal(sent.url, "/files/volumes/default/primary/copy");
  assert.deepEqual(JSON.parse(sent.body), {
    path: "notes.txt",
    target: { namespace: "default", name: "shelf", path: "notes.txt" },
  });

  // A copy leaves the source in place and names where the result landed.
  assert.deepEqual(rowNames(document), ["notes.txt"]);
  const status = document.getElementById("status").textContent;
  assert.ok(status.includes("shelf") && status.includes("notes.txt"), `状态报出目标：${status}`);
  assert.equal(document.getElementById("change-panel").hidden, true);
});

test("复制默认发往源卷时窗口仍按复制呈现，源条目留在列表里", async () => {
  const { document, window, network } = start({
    "GET /files/volumes/default/primary/entries": { status: 200, body: listing([entry("notes.txt", "file", 12)]) },
    "POST /files/volumes/default/primary/copy": {
      status: 200,
      body: change({ operation: "copy", path: "notes.txt", targetVolume: "default/primary", targetPath: "copy.txt" }),
    },
  });
  await openVolume(document, window);

  changeButton(document, "notes.txt", "copy").click();
  const select = document.getElementById("transfer-volume");

  // The default target is the volume the entry lives in, so a reader who only
  // types a new path copies inside the same volume.
  assert.equal(select.value, "default/primary");

  document.getElementById("transfer-path").value = "copy.txt";
  document.getElementById("transfer-submit").click();
  await settle();

  const sent = lastSent(network, "POST");
  assert.equal(sent.url, "/files/volumes/default/primary/copy");
  assert.deepEqual(JSON.parse(sent.body), {
    path: "notes.txt",
    target: { namespace: "default", name: "primary", path: "copy.txt" },
  });

  // The copy is presented as a copy: the source is still a row, and the window
  // never invents or removes a row on its own.
  assert.deepEqual(rowNames(document), ["notes.txt"]);
  assert.equal(document.getElementById("change-panel").hidden, true);
});

test("移动成功后源条目从列表消失", async () => {
  let answer = listing([entry("notes.txt", "file", 12), entry("nested", "directory", 0)]);
  const { document, window, network } = start({
    "GET /files/volumes/default/primary/entries": () => ({ status: 200, body: answer }),
    "POST /files/volumes/default/primary/move": {
      status: 200,
      body: change({ operation: "move", path: "notes.txt", targetVolume: "default/shelf", targetPath: "notes.txt" }),
    },
  });
  await openVolume(document, window);

  changeButton(document, "notes.txt", "move").click();
  document.getElementById("transfer-volume").value = "default/shelf";
  document.getElementById("transfer-path").value = "notes.txt";
  answer = listing([entry("nested", "directory", 0)]);
  document.getElementById("transfer-submit").click();
  await settle();

  const sent = lastSent(network, "POST");
  assert.equal(sent.url, "/files/volumes/default/primary/move");
  assert.deepEqual(JSON.parse(sent.body), {
    path: "notes.txt",
    target: { namespace: "default", name: "shelf", path: "notes.txt" },
  });

  // The source listing after the move is the volume's real content.
  assert.deepEqual(rowNames(document), ["nested"]);
  assert.equal(document.getElementById("change-panel").hidden, true);
});

test("删除先呈现影响范围；取消不发请求，确认后才带 confirm 提交", async () => {
  let answer = listing([entry("a.txt", "file", 2), entry("b.txt", "file", 3), entry("nested", "directory", 0)]);
  const { document, window, network } = start({
    "GET /files/volumes/default/primary/entries": () => ({ status: 200, body: answer }),
    "POST /files/volumes/default/primary/delete": (call) => {
      const body = JSON.parse(call.body);
      if (body.confirm) {
        return { status: 200, body: change({ operation: "delete", path: "nested", deleted: true }) };
      }
      return {
        status: 409,
        body: {
          error: {
            code: "delete_confirmation_required",
            message: "删除目录 nested 需要确认：将删除 4 个条目（含 1 个子目录），确认后生效",
          },
          confirmation: {
            volume: "default/primary",
            path: "nested",
            entryCount: 4,
            subdirectoryCount: 1,
            entries: ["a.txt", "b.txt", "inner"],
            truncated: false,
          },
        },
      };
    },
  });
  await openVolume(document, window);

  changeButton(document, "nested", "delete").click();
  await settle();

  // The first request states what the deletion removes and removes nothing: it
  // carries no confirmation.
  const preview = lastSent(network, "POST");
  assert.equal(preview.url, "/files/volumes/default/primary/delete");
  assert.deepEqual(JSON.parse(preview.body), { path: "nested", confirm: false });

  const counts = document.getElementById("delete-counts").textContent;
  assert.ok(counts.includes("4"), `确认列出条目数：${counts}`);
  assert.ok(counts.includes("1"), `确认列出子目录数：${counts}`);
  assert.deepEqual(
    document.getElementById("delete-entries").children.map((item) => item.textContent),
    ["a.txt", "b.txt", "inner"],
  );
  assert.equal(document.getElementById("delete-truncated").hidden, true);

  // Nothing was removed: the listing still holds the directory.
  assert.deepEqual(rowNames(document), ["a.txt", "b.txt", "nested"]);

  // Cancelling sends nothing at all; the unconfirmed request already removed
  // nothing.
  const beforeCancel = network.calls.length;
  document.getElementById("change-cancel").click();
  await settle();
  assert.equal(network.calls.length, beforeCancel, "取消不发请求");
  assert.equal(document.getElementById("change-panel").hidden, true);
  assert.deepEqual(rowNames(document), ["a.txt", "b.txt", "nested"]);

  // Confirming is the request that performs the deletion.
  answer = listing([entry("a.txt", "file", 2), entry("b.txt", "file", 3)]);
  changeButton(document, "nested", "delete").click();
  await settle();
  document.getElementById("delete-submit").click();
  await settle();

  const posts = network.calls.filter((call) => call.method === "POST");
  assert.deepEqual(JSON.parse(posts[posts.length - 1].body), { path: "nested", confirm: true });
  assert.deepEqual(rowNames(document), ["a.txt", "b.txt"]);
  assert.ok(document.getElementById("status").textContent.includes("nested"), "状态报出被删除的路径");
  assert.equal(document.getElementById("change-panel").hidden, true);
});

test("卷根的删除被拒绝，原因可读且内容不变", async () => {
  const { document, window, network } = start({
    "GET /files/volumes/default/primary/entries": { status: 200, body: listing([entry("a.txt", "file", 2)]) },
    "POST /files/volumes/default/primary/delete": {
      status: 400,
      body: { error: { code: "volume_root_protected", message: "卷根不可删除" } },
    },
  });
  await openVolume(document, window);
  assert.equal(document.getElementById("path").textContent, "卷根");

  document.getElementById("delete-current").click();
  await settle();

  const sent = lastSent(network, "POST");
  assert.equal(sent.url, "/files/volumes/default/primary/delete");
  assert.deepEqual(JSON.parse(sent.body), { path: "", confirm: false });

  // The refusal is the data plane's own reason, and nothing was confirmed.
  const status = document.getElementById("status");
  assert.ok(status.textContent.includes("卷根不可删除"), `拒绝原因可读：${status.textContent}`);
  assert.ok(status.classList.contains("blocked"));
  assert.equal(document.getElementById("change-panel").hidden, true);
  assert.equal(network.calls.filter((call) => call.method === "POST").length, 1);
  assert.deepEqual(rowNames(document), ["a.txt"]);
});

test("复制失败后显示响应原因，源仍在且不虚构目标条目", async () => {
  const { document, window, network } = start({
    "GET /files/volumes/default/primary/entries": { status: 200, body: listing([entry("notes.txt", "file", 12)]) },
    "POST /files/volumes/default/primary/copy": {
      status: 500,
      body: { error: { code: "files_write_failed", message: "写入目标 notes.txt 失败，未留下半份结果" } },
    },
  });
  await openVolume(document, window);

  changeButton(document, "notes.txt", "copy").click();
  document.getElementById("transfer-volume").value = "default/shelf";
  document.getElementById("transfer-path").value = "notes.txt";
  document.getElementById("transfer-submit").click();
  await settle();

  // The reason is the one the response carried, word for word.
  const status = document.getElementById("status");
  assert.ok(status.textContent.includes("未留下半份结果"), `原因逐字取自响应：${status.textContent}`);
  assert.ok(status.classList.contains("blocked"));

  // The source is still listed after the refresh, and the window invented no
  // target row: it never reads the target volume on its own.
  assert.deepEqual(rowNames(document), ["notes.txt"]);
  assert.ok(
    network.calls.filter((call) => call.url === "/files/volumes/default/primary/entries").length >= 2,
    "失败后源目录被重读",
  );
  assert.ok(
    network.calls.every((call) => !call.url.startsWith("/files/volumes/default/shelf")),
    "窗口不自行读取目标卷",
  );
});

test("条目过多时确认标注截断，并列出服务端给出的条目", async () => {
  const { document, window, network } = start({
    "GET /files/volumes/default/primary/entries": { status: 200, body: listing([entry("big", "directory", 0)]) },
    "POST /files/volumes/default/primary/delete": {
      status: 409,
      body: {
        error: {
          code: "delete_confirmation_required",
          message: "删除目录 big 需要确认：将删除 250 个条目（含 12 个子目录），确认后生效",
        },
        confirmation: {
          volume: "default/primary",
          path: "big",
          entryCount: 250,
          subdirectoryCount: 12,
          entries: ["alpha.txt", "beta.txt"],
          truncated: true,
        },
      },
    },
  });
  await openVolume(document, window);

  changeButton(document, "big", "delete").click();
  await settle();

  assert.deepEqual(
    document.getElementById("delete-entries").children.map((item) => item.textContent),
    ["alpha.txt", "beta.txt"],
  );
  const note = document.getElementById("delete-truncated");
  assert.equal(note.hidden, false, "截断被标注");
  assert.ok(note.textContent.includes("2"), `标注报出实际列出的条目数：${note.textContent}`);
  assert.equal(network.calls.filter((call) => call.method === "POST").length, 1);
});

test("删除当前目录后窗口回到上级目录，路径与列表都随卷的真实内容", async () => {
  let root = listing([entry("docs", "directory", 0)]);
  const inner = {
    volume: "default/primary",
    path: "docs",
    observedAt: "2026-04-03T10:16:00Z",
    limit: 500,
    total: 1,
    truncated: false,
    entries: [entry("inner.txt", "file", 3)],
  };
  const { document, window, network } = start({
    "GET /files/volumes/default/primary/entries": () => ({ status: 200, body: root }),
    "GET /files/volumes/default/primary/entries?path=docs": { status: 200, body: inner },
    "POST /files/volumes/default/primary/delete": (call) => {
      const body = JSON.parse(call.body);
      if (body.confirm) {
        return { status: 200, body: change({ operation: "delete", path: "docs", deleted: true }) };
      }
      return {
        status: 409,
        body: {
          error: { code: "delete_confirmation_required", message: "删除目录 docs 需要确认：将删除 1 个条目，确认后生效" },
          confirmation: {
            volume: "default/primary",
            path: "docs",
            entryCount: 1,
            subdirectoryCount: 0,
            entries: ["inner.txt"],
            truncated: false,
          },
        },
      };
    },
  });
  await openVolume(document, window);

  findByClass(document.getElementById("entries").children[0], "entry-name").click();
  await settle();
  assert.equal(document.getElementById("path").textContent, "/docs");

  document.getElementById("delete-current").click();
  await settle();
  assert.deepEqual(JSON.parse(lastSent(network, "POST").body), { path: "docs", confirm: false });

  // The volume no longer holds docs once the confirmed deletion ran, so the
  // parent listing is what the window lands on.
  root = listing([]);
  document.getElementById("delete-submit").click();
  await settle();

  assert.deepEqual(JSON.parse(lastSent(network, "POST").body), { path: "docs", confirm: true });
  assert.equal(document.getElementById("path").textContent, "卷根");
  assert.deepEqual(rowNames(document), []);
  assert.ok(document.getElementById("status").textContent.includes("docs"), "状态报出被删除的路径");
});
