// FILE-TC-6 from the window side: the window uploads into the directory it is
// browsing and shows the transfer's progress while it runs and once it ends; a
// same-name file comes back as a conflict that waits for confirmation and
// leaves the target untouched until then; cancelling and a mid-transfer failure
// end the upload without a completed file; a download reports progress against
// the declared size and reads the reason a missing or unreadable file answers
// (FILE-AC-4, FILE-C-4).

import assert from "node:assert/strict";
import test from "node:test";

import { createFileManager } from "../../../pkg/product/assets/app.js";
import {
  deferred,
  fakeFetch,
  fakeIntervals,
  fakeWindow,
  findByClass,
  settle,
  streamReply,
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

// modified is the instant every entry and conflict carries, so the rendered
// time is compared with one exact label.
const modified = "2026-04-02T09:30:00Z";

// route is the data-plane address of the open volume.
const route = "/files/volumes/default/alice-data";

// declared is the size of the two-chunk upload most cases send: one whole
// window chunk plus a remainder, so progress is observable between chunks.
const chunk = 512 * 1024;

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
function listing(path, entries) {
  return {
    volume: "default/alice-data",
    path,
    observedAt: "2026-04-02T09:31:00Z",
    limit: 500,
    total: entries.length,
    truncated: false,
    entries,
  };
}

// uploadAnswer is one transfer snapshot the data plane would serve: the open
// session, a progress read, or a settled session.
function uploadAnswer(fields) {
  return {
    id: "t-1",
    direction: "upload",
    volume: "default/alice-data",
    path: "",
    name: "report.bin",
    target: "report.bin",
    state: "open",
    size: 0,
    received: 0,
    overwrite: false,
    createdAt: "2026-04-02T09:31:00Z",
    updatedAt: "2026-04-02T09:31:00Z",
    ...fields,
  };
}

// start mounts the window against one data-plane answer per route and records
// the files the window hands to the reader's disk.
function start({ answer, routes }) {
  const document = windowDocument();
  const window = fakeWindow(frameAddress("kos-console=" + encodeURIComponent(consoleOrigin) + "&kos-window=w-1"));
  const network = fakeFetch({ "GET /files/window": { status: 200, body: answer }, ...routes });
  const clock = fakeIntervals();
  const saved = [];
  createFileManager({
    document,
    window,
    fetch: network.fetch,
    setInterval: clock.setInterval,
    save: (name, bytes, contentType) => saved.push({ name, bytes: new Uint8Array(bytes), contentType }),
  }).start();
  return { document, window, network, clock, saved };
}

// openWindow delivers the console's init and lets the window read its volumes.
async function openWindow(document, window) {
  window.deliver({ origin: consoleOrigin, source: window.parent, data: init("window-token") });
  await settle();
}

// openButton is the control one volume renders to open it for browsing.
function openButton(document) {
  const button = findByClass(document.getElementById("volumes"), "volume-open");
  assert.ok(button, "the volume list offers a way to open a volume");
  return button;
}

// downloadButton finds the download control one file entry renders, so a case
// downloads the entry the listing shows instead of a position in the list.
function downloadButton(document, name) {
  for (const item of document.getElementById("entries").children) {
    const button = findByClass(item, "entry-download");
    if (button && button.dataset.entry === name) return button;
  }
  throw new Error(`no download control for ${name}`);
}

// pickFile hands one file to the window the way the picker does: the document's
// own input carries it and the change a browser fires follows.
function pickFile(document, file) {
  const input = document.getElementById("upload-file");
  input.files = [file];
  input.dispatch("change");
}

// sendBytes reads the upload bodies back in the order the window sent them.
function sendBytes(network) {
  const parts = network.calls.filter((call) => call.method === "PUT").map((call) => Buffer.from(call.body));
  return Buffer.concat(parts);
}

// progressValue reads the rendered progress the reader compares with the total.
function progressValue(document) {
  return document.getElementById("transfer-progress").value;
}

function progressMax(document) {
  return document.getElementById("transfer-progress").max;
}

// bytesOf fills one source buffer with a non-repeating pattern, so an uploaded
// byte that differs is not mistaken for padding.
function bytesOf(size) {
  const bytes = new Uint8Array(size);
  for (let index = 0; index < size; index += 1) bytes[index] = index % 251;
  return bytes;
}

test("上传写入浏览所在的目录，进度在传输中可读且结束后与文件总量一致", async () => {
  const bytes = bytesOf(chunk + 777);
  const file = new File([bytes], "report.bin", { type: "application/octet-stream" });
  const hold = deferred();
  let isUploaded = false;
  const { document, window, network } = start({
    answer: { view: "list", initialPath: "/docs", volumes: [volume] },
    routes: {
      [`GET ${route}/entries?path=docs`]: () => ({
        status: 200,
        body: listing("docs", isUploaded ? [entry("old.txt", "file", 3), entry("report.bin", "file", bytes.length)] : [entry("old.txt", "file", 3)]),
      }),
      [`POST ${route}/transfers`]: {
        status: 201,
        body: uploadAnswer({ path: "docs", name: "report.bin", target: "docs/report.bin", size: bytes.length }),
      },
      [`PUT ${route}/transfers/t-1/content`]: (call) =>
        call.body.byteLength === bytes.length - chunk
          ? hold.promise.then(() => {
              isUploaded = true;
              return {
                status: 200,
                body: uploadAnswer({ path: "docs", name: "report.bin", target: "docs/report.bin", size: bytes.length, received: bytes.length, state: "completed" }),
              };
            })
          : {
              status: 202,
              body: uploadAnswer({ path: "docs", name: "report.bin", target: "docs/report.bin", size: bytes.length, received: call.body.byteLength }),
            },
      [`GET ${route}/transfers/t-1`]: {
        status: 200,
        body: uploadAnswer({ path: "docs", name: "report.bin", target: "docs/report.bin", size: bytes.length, received: chunk }),
      },
    },
  });
  await openWindow(document, window);

  openButton(document).click();
  await settle();
  assert.equal(document.getElementById("upload").hidden, false);

  pickFile(document, file);
  await settle();

  // The session is opened for the browsed directory with the file's whole size
  // and no confirmation, and the first chunk is already on its way.
  const opened = network.calls.find((call) => call.method === "POST");
  assert.equal(opened.url, `${route}/transfers`);
  assert.deepEqual(JSON.parse(opened.body), { path: "docs", name: "report.bin", size: bytes.length, overwrite: false });
  assert.equal(opened.headers.Authorization, "Bearer window-token");
  assert.equal(opened.credentials, "omit");

  // While the second part is still in flight the progress reported by the
  // service is on screen, short of the file's total.
  assert.equal(network.calls.filter((call) => call.method === "PUT").length, 2, "the second part is in flight");
  assert.ok(network.calls.some((call) => call.method === "GET" && call.url === `${route}/transfers/t-1`), "progress is read between chunks");
  assert.equal(progressMax(document), bytes.length);
  assert.equal(progressValue(document), chunk);
  assert.match(document.getElementById("transfer-meta").textContent, /99%/);
  assert.equal(document.getElementById("transfer-state").textContent, "上传中");
  assert.equal(document.getElementById("transfer-cancel").hidden, false);

  hold.resolve();
  await settle();

  // The whole content reached the service and the progress ends at the total.
  assert.equal(progressMax(document), bytes.length);
  assert.equal(progressValue(document), bytes.length);
  assert.match(document.getElementById("transfer-meta").textContent, /100%/);
  assert.equal(document.getElementById("transfer-state").textContent, "已完成");
  assert.equal(document.getElementById("transfer-cancel").hidden, true);
  assert.equal(document.getElementById("transfer-confirm").hidden, true);
  assert.equal(sendBytes(network).length, bytes.length);
  assert.equal(Buffer.compare(sendBytes(network), Buffer.from(bytes)), 0, "the uploaded bytes are the source");

  // The listing is read again, so the new file appears where it was written.
  const rendered = document.getElementById("entries").textContent;
  assert.match(rendered, /report\.bin/);
  assert.match(rendered, /512\.8 KB/);
  assert.match(document.getElementById("status").textContent, /上传完成/);
});

test("同名文件先确认：未确认前不发内容，取消后目标不变，确认后以覆盖会话重传", async () => {
  const existing = "original body";
  const bytes = new TextEncoder().encode("new!!");
  let listingBody = listing("", [entry("notes.txt", "file", existing.length)]);
  const { document, window, network } = start({
    answer: { view: "list", initialPath: "", volumes: [volume] },
    routes: {
      [`GET ${route}/entries`]: () => ({ status: 200, body: listingBody }),
      [`POST ${route}/transfers`]: (call) => {
        const request = JSON.parse(call.body);
        return {
          status: 201,
          body: uploadAnswer({
            name: "notes.txt",
            target: "notes.txt",
            size: request.size,
            overwrite: request.overwrite,
            state: request.overwrite ? "open" : "conflict",
            conflict: { size: existing.length, modifiedAt: modified },
            reason: request.overwrite ? undefined : "目标位置已存在同名文件 notes.txt：确认覆盖后才能上传",
          }),
        };
      },
      [`PUT ${route}/transfers/t-1/content`]: {
        status: 200,
        body: uploadAnswer({ name: "notes.txt", target: "notes.txt", size: bytes.length, received: bytes.length, state: "completed" }),
      },
      [`DELETE ${route}/transfers/t-1`]: {
        status: 200,
        body: uploadAnswer({ name: "notes.txt", target: "notes.txt", size: bytes.length, state: "cancelled" }),
      },
    },
  });
  await openWindow(document, window);
  openButton(document).click();
  await settle();

  pickFile(document, new File([bytes], "notes.txt"));
  await settle();

  // The conflict is on screen with the existing file's size and the service's
  // reason; no content is sent and the confirmation is offered.
  assert.equal(network.calls.filter((call) => call.method === "PUT").length, 0, "a conflict sends no content");
  assert.equal(document.getElementById("transfer-state").textContent, "等待确认");
  const meta = document.getElementById("transfer-meta").textContent;
  assert.match(meta, /确认覆盖后才能上传/);
  assert.match(meta, /13 B/);
  assert.equal(document.getElementById("transfer-confirm").hidden, false);
  assert.equal(document.getElementById("transfer-cancel").hidden, false);

  // Cancelling before the confirmation stops the session and leaves the file
  // the window browses exactly as it was.
  document.getElementById("transfer-cancel").click();
  await settle();
  const cancelled = network.calls.find((call) => call.method === "DELETE");
  assert.equal(cancelled.url, `${route}/transfers/t-1`);
  assert.equal(document.getElementById("transfer-state").textContent, "已取消");
  assert.match(document.getElementById("entries").textContent, /notes\.txt/);
  assert.match(document.getElementById("entries").textContent, /13 B/);
  assert.equal(network.calls.filter((call) => call.method === "PUT").length, 0);

  // Picking the same name again answers a conflict again; the confirmation
  // re-opens the session with overwrite and only then sends the content.
  listingBody = listing("", [entry("notes.txt", "file", bytes.length)]);
  pickFile(document, new File([bytes], "notes.txt"));
  await settle();
  document.getElementById("transfer-confirm").click();
  await settle();

  const posts = network.calls.filter((call) => call.method === "POST").map((call) => JSON.parse(call.body));
  assert.equal(posts.length, 3);
  assert.deepEqual(posts[0], { path: "", name: "notes.txt", size: bytes.length, overwrite: false });
  assert.deepEqual(posts[2], { path: "", name: "notes.txt", size: bytes.length, overwrite: true });
  assert.equal(network.calls.filter((call) => call.method === "PUT").length, 1);
  assert.equal(Buffer.compare(sendBytes(network), Buffer.from(bytes)), 0);
  assert.equal(progressValue(document), bytes.length);
  assert.equal(progressMax(document), bytes.length);
  assert.equal(document.getElementById("transfer-state").textContent, "已完成");
  assert.match(document.getElementById("entries").textContent, /5 B/);
});

test("传输期间出现的同名文件转为确认，确认后以覆盖会话完成", async () => {
  const bytes = bytesOf(chunk + 10);
  const reason = "目标位置在上传期间出现同名文件 report.bin：确认覆盖后才能上传";
  let listingBody = listing("", [entry("keep.txt", "file", 2)]);
  let puts = 0;
  const { document, window, network } = start({
    answer: { view: "list", initialPath: "", volumes: [volume] },
    routes: {
      [`GET ${route}/entries`]: () => ({ status: 200, body: listingBody }),
      [`POST ${route}/transfers`]: (call) => {
        const request = JSON.parse(call.body);
        return { status: 201, body: uploadAnswer({ size: request.size, overwrite: request.overwrite }) };
      },
      [`PUT ${route}/transfers/t-1/content`]: (call) => {
        puts += 1;
        if (puts === 1 || puts === 3) {
          return { status: 202, body: uploadAnswer({ size: bytes.length, received: call.body.byteLength }) };
        }
        if (puts === 2) {
          return {
            status: 409,
            body: {
              error: { code: "upload_conflict", message: reason },
              transfer: uploadAnswer({
                state: "conflict",
                size: bytes.length,
                received: bytes.length,
                reason,
                conflict: { size: 9, modifiedAt: modified },
              }),
            },
          };
        }
        listingBody = listing("", [entry("keep.txt", "file", 2), entry("report.bin", "file", bytes.length)]);
        return { status: 200, body: uploadAnswer({ size: bytes.length, received: bytes.length, state: "completed" }) };
      },
      [`GET ${route}/transfers/t-1`]: {
        status: 200,
        body: uploadAnswer({ size: bytes.length, received: chunk }),
      },
    },
  });
  await openWindow(document, window);
  openButton(document).click();
  await settle();

  pickFile(document, new File([bytes], "report.bin"));
  await settle();

  // The file that appeared during the transfer is never replaced silently: the
  // window shows the service's reason and asks before sending the content
  // again.
  assert.equal(document.getElementById("transfer-state").textContent, "等待确认");
  assert.match(document.getElementById("transfer-meta").textContent, /上传期间出现同名文件/);
  assert.match(document.getElementById("transfer-meta").textContent, /9 B/);
  assert.equal(document.getElementById("transfer-confirm").hidden, false);
  assert.equal(progressValue(document), 0, "the staged bytes were dropped, so the resend starts over");

  document.getElementById("transfer-confirm").click();
  await settle();

  const posts = network.calls.filter((call) => call.method === "POST").map((call) => JSON.parse(call.body));
  assert.equal(posts.length, 2);
  assert.equal(posts[0].overwrite, false);
  assert.equal(posts[1].overwrite, true);
  assert.equal(puts, 4, "the confirmed session sends both chunks again");
  assert.equal(progressValue(document), bytes.length);
  assert.equal(progressMax(document), bytes.length);
  assert.equal(document.getElementById("transfer-state").textContent, "已完成");
  assert.match(document.getElementById("entries").textContent, /report\.bin/);
});

test("进行中的上传取消走 DELETE，中途失败给出原因，目标位置都不留半份文件", async () => {
  const bytes = bytesOf(2 * chunk + 5);
  const hold = deferred();
  let puts = 0;
  let isCancelled = false;
  const { document, window, network } = start({
    answer: { view: "list", initialPath: "", volumes: [volume] },
    routes: {
      [`GET ${route}/entries`]: {
        status: 200,
        body: listing("", [entry("keep.txt", "file", 2)]),
      },
      [`POST ${route}/transfers`]: (call) => {
        const request = JSON.parse(call.body);
        return { status: 201, body: uploadAnswer({ size: request.size, name: request.name, target: request.name }) };
      },
      [`PUT ${route}/transfers/t-1/content`]: (call) => {
        puts += 1;
        if (puts === 1) {
          return hold.promise.then(() => ({ status: 202, body: uploadAnswer({ size: bytes.length, received: call.body.byteLength }) }));
        }
        return {
          status: 400,
          body: {
            error: { code: "transfer_size_mismatch", message: "上传中断：内容没有收全，目标位置未写入" },
            transfer: uploadAnswer({ size: bytes.length, received: 0, state: "failed", reason: "上传中断：内容没有收全，目标位置未写入" }),
          },
        };
      },
      [`DELETE ${route}/transfers/t-1`]: () => {
        isCancelled = true;
        return { status: 200, body: uploadAnswer({ size: bytes.length, state: "cancelled" }) };
      },
      [`GET ${route}/transfers/t-1`]: {
        status: 200,
        body: uploadAnswer({ size: bytes.length, received: chunk }),
      },
    },
  });
  await openWindow(document, window);
  openButton(document).click();
  await settle();

  // Cancelling while a chunk is in flight ends the session with DELETE; the
  // window stops sending and re-reads the directory it was writing into.
  pickFile(document, new File([bytes], "half.bin"));
  await settle();
  assert.equal(puts, 1);
  document.getElementById("transfer-cancel").click();
  await settle();
  const cancelled = network.calls.find((call) => call.method === "DELETE");
  assert.equal(cancelled.url, `${route}/transfers/t-1`);
  assert.equal(isCancelled, true);
  hold.resolve();
  await settle();
  assert.equal(puts, 1, "no chunk follows the cancellation");
  assert.equal(document.getElementById("transfer-state").textContent, "已取消");
  assert.doesNotMatch(document.getElementById("entries").textContent, /half\.bin/);

  // A transfer the service stops mid-way reports its reason and leaves the
  // directory without the name it was writing.
  pickFile(document, new File([bytes], "half.bin"));
  await settle();
  assert.match(document.getElementById("status").textContent, /上传中断/);
  assert.ok(document.getElementById("status").classList.contains("blocked"));
  assert.equal(document.getElementById("transfer-state").textContent, "失败");
  document.getElementById("refresh").click();
  await settle();
  assert.doesNotMatch(document.getElementById("entries").textContent, /half\.bin/);
  assert.match(document.getElementById("entries").textContent, /keep\.txt/);
});

test("下载进度与文件总量一致，缺失或不可读文件的拒绝原因可读", async () => {
  const payload = new TextEncoder().encode("0123456789".slice(0, 9));
  const hold = deferred();
  const { document, window, network, saved } = start({
    answer: { view: "list", initialPath: "", volumes: [volume] },
    routes: {
      [`GET ${route}/entries`]: {
        status: 200,
        // ok.bin's listed size is stale: another window replaced the file after
        // this listing, so only the declared length names the real total.
        body: listing("", [entry("gone.txt", "file", 12), entry("secret.txt", "file", 12), entry("ok.bin", "file", 12)]),
      },
      [`GET ${route}/download?path=gone.txt`]: {
        status: 404,
        body: { error: { code: "path_not_found", message: "路径 gone.txt 不存在" } },
      },
      [`GET ${route}/download?path=secret.txt`]: {
        status: 403,
        body: { error: { code: "file_unreadable", message: "文件 secret.txt 不可读：权限不足" } },
      },
      [`GET ${route}/download?path=ok.bin`]: streamReply(200, {
        total: payload.length,
        chunks: [payload.subarray(0, 4), payload.subarray(4)],
        gates: [null, hold.promise],
      }),
    },
  });
  await openWindow(document, window);
  openButton(document).click();
  await settle();

  // A file another window removed is refused with the service's reason, and
  // nothing is shown as a completed download.
  downloadButton(document, "gone.txt").click();
  await settle();
  assert.match(document.getElementById("status").textContent, /路径 gone\.txt 不存在/);
  assert.ok(document.getElementById("status").classList.contains("blocked"));
  assert.equal(document.getElementById("transfer-state").textContent, "失败");
  assert.equal(saved.length, 0);

  downloadButton(document, "secret.txt").click();
  await settle();
  assert.match(document.getElementById("status").textContent, /不可读：权限不足/);
  assert.equal(saved.length, 0);

  // A readable file declares its whole size, so the progress the window shows
  // while the body is still arriving ends exactly at that total.
  downloadButton(document, "ok.bin").click();
  await settle();
  assert.equal(progressMax(document), payload.length);
  assert.equal(progressValue(document), 4);
  assert.match(document.getElementById("transfer-meta").textContent, /4 B \/ 9 B/);
  assert.equal(document.getElementById("transfer-state").textContent, "下载中");
  const request = network.calls.find((call) => call.url === `${route}/download?path=ok.bin`);
  assert.equal(request.headers.Authorization, "Bearer window-token");
  assert.equal(request.credentials, "omit");

  hold.resolve();
  await settle();
  assert.equal(progressValue(document), payload.length);
  assert.match(document.getElementById("transfer-meta").textContent, /100%/);
  assert.equal(document.getElementById("transfer-state").textContent, "已完成");
  assert.equal(saved.length, 1);
  assert.equal(saved[0].name, "ok.bin");
  assert.equal(Buffer.compare(Buffer.from(saved[0].bytes), Buffer.from(payload)), 0);
});
