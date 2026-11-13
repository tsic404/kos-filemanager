// File-manager window: the application side of the handshake that delivers the
// window's application token, the authorized volume list the data plane serves,
// the browse and read-only preview of one volume's current content, and the
// uploads and downloads that write and read inside it
// (FILE-C-1, FILE-C-3, FILE-C-4, DASH-C-27, KOSD-C-75).
//
// The token stays in this closure: it is never written to storage, the document
// or a message. Reads go to this origin's data plane without the console cookie,
// which the browser never hands to an application origin (KOSD-C-75).

// The message names the console channel uses (KOSD-C-75).
const READY_MESSAGE = "ready";
const ALIVE_MESSAGE = "alive";
const INIT_MESSAGE = "init";

// ALIVE_INTERVAL_MS stays well under the console's silence window, so a running
// window never looks stopped (DASH-C-74).
const ALIVE_INTERVAL_MS = 15000;

// VOLUMES_PATH is this product's own data plane, on the application origin
// (NG10). It is not a control-plane resource API.
const VOLUMES_PATH = "/files/window";

// The two views a window may open with (DASH-C-27).
const VIEW_LIST = "list";
const VIEW_GRID = "grid";

// PATH_ROOT_LABEL names the location when no initial path is stored.
const PATH_ROOT_LABEL = "卷根";

// PHASE_LABELS render the volume projection's phase; an unknown phase stays
// visible instead of being read as usable (FILE-C-2).
const PHASE_LABELS = Object.freeze({
  Bound: "已绑定",
  Pending: "未绑定",
  Lost: "已丢失",
});

// KIND_LABELS render the entry type the data plane reports (FILE-C-3).
const KIND_LABELS = Object.freeze({
  file: "文件",
  directory: "目录",
  symlink: "链接",
  other: "其他",
});

// KIND_DIRECTORY is the one entry type a click descends into.
const KIND_DIRECTORY = "directory";

// KIND_FILE is the one entry type the window offers to download.
const KIND_FILE = "file";

// UPLOAD_CHUNK_BYTES bounds one content request. Sending the file in parts is
// what keeps the service's progress readable while the transfer runs
// (FILE-C-4).
const UPLOAD_CHUNK_BYTES = 512 * 1024;

// TRANSFER_STATE_LABELS render the state the data plane reports for one
// transfer; an unknown state stays visible instead of reading as success
// (FILE-C-4).
const TRANSFER_STATE_LABELS = Object.freeze({
  opening: "正在打开",
  open: "上传中",
  uploading: "上传中",
  downloading: "下载中",
  conflict: "等待确认",
  completed: "已完成",
  cancelled: "已取消",
  failed: "失败",
});

// SETTLED_TRANSFER_STATES are the states a transfer no longer moves out of.
const SETTLED_TRANSFER_STATES = new Set(["completed", "cancelled", "failed"]);

// TOKEN_CODES are the refusals that end this window: the token is gone and a
// reopened window is the only way forward (KOSD-C-75).
const TOKEN_CODES = new Set(["unauthenticated", "token_invalid", "token_foreign_app"]);

// BYTE_STEP and BYTE_UNITS render one size with a unit the reader can compare.
const BYTE_STEP = 1024;
const BYTE_UNITS = Object.freeze(["B", "KB", "MB", "GB"]);

// createFileManager binds the window to one document, one frame window and one
// fetch. Every dependency is injected so the handshake, the reads, both views
// and the transfers can be exercised without a browser.
export function createFileManager({
  document: doc,
  window: frame,
  fetch: read = globalThis.fetch,
  setInterval: startInterval = globalThis.setInterval,
  save: persist = (name, bytes, contentType) => saveDownloadedFile(doc, name, bytes, contentType),
}) {
  const state = {
    consoleOrigin: "",
    windowId: "",
    token: "",
    view: VIEW_LIST,
    initialPath: "",
    isInitialPathApplied: false,
    volumes: [],
    problems: [],
    volume: null,
    path: "",
    entries: [],
    limit: 0,
    total: 0,
    isTruncated: false,
    hasListing: false,
    preview: null,
    transfer: null,
    upload: null,
  };

  const elements = {
    status: doc.getElementById("status"),
    path: doc.getElementById("path"),
    volumes: doc.getElementById("volumes"),
    volumesEmpty: doc.getElementById("volumes-empty"),
    count: doc.getElementById("count"),
    problems: doc.getElementById("problems"),
    viewList: doc.getElementById("view-list"),
    viewGrid: doc.getElementById("view-grid"),
    browser: doc.getElementById("browser-panel"),
    entries: doc.getElementById("entries"),
    entriesEmpty: doc.getElementById("entries-empty"),
    entriesCount: doc.getElementById("entries-count"),
    truncated: doc.getElementById("truncated"),
    refresh: doc.getElementById("refresh"),
    back: doc.getElementById("back"),
    root: doc.getElementById("root"),
    previewPanel: doc.getElementById("preview-panel"),
    previewMeta: doc.getElementById("preview-meta"),
    previewText: doc.getElementById("preview-text"),
    previewImage: doc.getElementById("preview-image"),
    previewCut: doc.getElementById("preview-cut"),
    previewClose: doc.getElementById("preview-close"),
    upload: doc.getElementById("upload"),
    uploadFile: doc.getElementById("upload-file"),
    transferPanel: doc.getElementById("transfer-panel"),
    transferName: doc.getElementById("transfer-name"),
    transferState: doc.getElementById("transfer-state"),
    transferProgress: doc.getElementById("transfer-progress"),
    transferMeta: doc.getElementById("transfer-meta"),
    transferConfirm: doc.getElementById("transfer-confirm"),
    transferCancel: doc.getElementById("transfer-cancel"),
    transferDismiss: doc.getElementById("transfer-dismiss"),
  };

  let alive = 0;

  // start resolves the console from the frame address and opens the handshake.
  // An address without the console origin names no permissible target, so the
  // window reports why it cannot start instead of posting into the dark.
  function start() {
    const search = addressSearch(frame.location && frame.location.href);
    if (!search) {
      setStatus("窗口地址无效，应用不能启动", true);
      return;
    }
    state.consoleOrigin = originOf(search.get("kos-console"));
    state.windowId = (search.get("kos-window") || "").trim();
    if (!state.consoleOrigin) {
      setStatus("窗口地址没有控制台来源，应用不能启动", true);
      return;
    }
    elements.viewList.addEventListener("click", () => setView(VIEW_LIST));
    elements.viewGrid.addEventListener("click", () => setView(VIEW_GRID));
    elements.back.addEventListener("click", leaveVolume);
    elements.root.addEventListener("click", () => void browse(""));
    elements.refresh.addEventListener("click", () => void browse(state.path));
    elements.previewClose.addEventListener("click", clearPreview);
    elements.upload.addEventListener("click", () => elements.uploadFile.click());
    elements.uploadFile.addEventListener("change", () => {
      const files = elements.uploadFile.files;
      const selected = files && files[0];
      // The picker is reset so choosing the same file again fires a change;
      // the selected file is read before the value is cleared.
      elements.uploadFile.value = "";
      if (selected) {
        void uploadFile(selected);
      }
    });
    elements.transferConfirm.addEventListener("click", () => void confirmOverwrite());
    elements.transferCancel.addEventListener("click", () => void cancelTransfer());
    elements.transferDismiss.addEventListener("click", dismissTransfer);
    setView(VIEW_LIST);
    renderVolumes();
    renderBrowser();
    frame.addEventListener("message", receive);
    frame.parent.postMessage(envelope(READY_MESSAGE), state.consoleOrigin);
  }

  // receive judges one inbound message: only an init from the console that
  // embedded this window is accepted, and only once (KOSD-C-75).
  function receive(event) {
    if (!event || event.origin !== state.consoleOrigin || event.source !== frame.parent) {
      return;
    }
    const data = event.data;
    if (!data || data.type !== INIT_MESSAGE || state.token) {
      return;
    }
    if (typeof data.token !== "string" || data.token === "") {
      setStatus("控制台返回的令牌无效", true);
      return;
    }
    state.token = data.token;
    if (typeof data.theme === "string" && data.theme !== "") {
      doc.documentElement.dataset.theme = data.theme;
    }
    armAlive();
    void loadWindow();
  }

  // armAlive reports the window as running for as long as the console listens;
  // the console ends the frame on its own when the token expires.
  function armAlive() {
    if (alive) {
      return;
    }
    alive = startInterval(
      () => frame.parent.postMessage(envelope(ALIVE_MESSAGE), state.consoleOrigin),
      ALIVE_INTERVAL_MS,
    );
  }

  function envelope(type) {
    const message = { type };
    if (state.windowId) {
      message.window = state.windowId;
    }
    return message;
  }

  // loadWindow reads the volumes this account may open and the view preferences
  // the account stored. The read carries the window token and no console
  // cookie: the console session never reaches this origin (KOSD-C-75).
  async function loadWindow() {
    setStatus("正在读取授权卷…");
    let response;
    try {
      response = await read(VOLUMES_PATH, {
        headers: { Authorization: `Bearer ${state.token}` },
        credentials: "omit",
      });
    } catch {
      setStatus("读取授权卷失败，请稍后重试", true);
      return;
    }
    if (response.status === 401) {
      setStatus("窗口令牌已失效，请重新打开窗口", true);
      renderVolumes();
      return;
    }
    if (response.status === 403) {
      setStatus("窗口令牌不属于本应用，请重新打开窗口", true);
      renderVolumes();
      return;
    }
    if (!response.ok) {
      setStatus(await failureText(response), true);
      return;
    }
    let answer;
    try {
      answer = await response.json();
    } catch {
      setStatus("读取授权卷失败：应答无法解析", true);
      return;
    }
    applyAnswer(answer);
    setView(state.view);
    renderVolumes();
    renderBrowser();
    setStatus(`${state.volumes.length} 个授权卷`);
  }

  // failureText keeps the data plane's own reason readable instead of replacing
  // it with a status code (FILE-C-2).
  async function failureText(response) {
    try {
      const body = await response.json();
      const message = body && body.error && body.error.message;
      if (typeof message === "string" && message !== "") {
        return `读取授权卷失败：${message}`;
      }
    } catch {
      // The status code below is the fallback when the body is not readable.
    }
    return `读取授权卷失败（${response.status}）`;
  }

  // applyAnswer adopts the data-plane answer: the stored view and initial path,
  // and the volume list already scoped to this account. A view outside the
  // declared vocabulary falls back to the list instead of failing the window
  // (DASH-C-27).
  function applyAnswer(answer) {
    const body = answer && typeof answer === "object" ? answer : {};
    state.view = body.view === VIEW_GRID ? VIEW_GRID : VIEW_LIST;
    state.initialPath = typeof body.initialPath === "string" ? body.initialPath.trim() : "";
    state.isInitialPathApplied = false;
    state.volumes = Array.isArray(body.volumes) ? body.volumes.filter(isVolumeEntry) : [];
    state.problems = Array.isArray(body.problems) ? body.problems.filter((entry) => typeof entry === "string") : [];
    leaveVolume();
  }

  // setView switches the volume list and the directory between the two declared
  // views. It is presentation state: no preference is written from here.
  function setView(view) {
    state.view = view === VIEW_GRID ? VIEW_GRID : VIEW_LIST;
    elements.volumes.className = "volumes " + state.view;
    elements.entries.className = "entries " + state.view;
    elements.viewList.setAttribute("aria-pressed", String(state.view === VIEW_LIST));
    elements.viewGrid.setAttribute("aria-pressed", String(state.view === VIEW_GRID));
  }

  // renderPath names the location: the stored initial path while no volume is
  // open, then the directory inside the open volume.
  function renderPath() {
    if (state.volume === null) {
      elements.path.textContent = state.initialPath || PATH_ROOT_LABEL;
      return;
    }
    elements.path.textContent = state.path === "" ? PATH_ROOT_LABEL : "/" + state.path;
  }

  // renderVolumes lays out exactly the entries the data plane served. An entry
  // another account owns is not in the answer, so it never becomes a row
  // (FILE-C-1).
  function renderVolumes() {
    elements.volumes.replaceChildren();
    for (const entry of state.volumes) {
      elements.volumes.append(volumeNode(entry));
    }
    elements.volumesEmpty.hidden = state.volumes.length !== 0;
    elements.count.textContent = `${state.volumes.length} 个授权卷`;
    if (elements.problems) {
      const problems = state.problems || [];
      elements.problems.textContent = problems.join("；");
      elements.problems.hidden = problems.length === 0;
    }
  }

  function volumeNode(entry) {
    const item = doc.createElement("li");
    item.className = "volume";
    const name = doc.createElement("h3");
    name.className = "volume-name";
    const open = doc.createElement("button");
    open.type = "button";
    open.className = "volume-open";
    open.textContent = entry.name || "未命名卷";
    open.dataset.volume = `${entry.namespace}/${entry.name}`;
    open.addEventListener("click", () => void openVolume(entry));
    name.append(open);
    item.append(name);

    const meta = doc.createElement("p");
    meta.className = "volume-meta";
    const namespace = typeof entry.namespace === "string" && entry.namespace !== "" ? entry.namespace + "/" : "";
    meta.textContent = namespace + phaseLabel(entry.phase);
    if (entry.stale === true) {
      meta.textContent += " · 陈旧";
    }
    item.append(meta);

    if (typeof entry.reason === "string" && entry.reason !== "") {
      const reason = doc.createElement("p");
      reason.className = "volume-reason";
      reason.textContent = entry.reason;
      item.append(reason);
    }
    return item;
  }

  function phaseLabel(phase) {
    return PHASE_LABELS[phase] || "状态未知";
  }

  // openVolume starts browsing one volume at the stored initial path; a later
  // volume opens at its root because the preference names the window's first
  // location, not a memory per volume.
  async function openVolume(entry) {
    if (typeof entry.namespace !== "string" || entry.namespace === "") {
      setStatus("卷缺少命名空间，无法浏览", true);
      return;
    }
    state.volume = entry;
    state.path = "";
    state.entries = [];
    state.limit = 0;
    state.total = 0;
    state.isTruncated = false;
    state.hasListing = false;
    state.preview = null;
    renderBrowser();
    const opening = state.isInitialPathApplied ? "" : initialBrowsePath();
    state.isInitialPathApplied = true;
    await browse(opening);
  }

  // initialBrowsePath turns the stored initial path into the first directory
  // this window browses. The preference is a location path ("/documents") while
  // the data plane addresses relative paths, so the leading slash is dropped; a
  // path with a parent step is not addressable and falls back to the volume root.
  function initialBrowsePath() {
    if (state.initialPath === "") {
      return "";
    }
    const segments = [];
    for (const segment of state.initialPath.split("/")) {
      if (segment === "" || segment === ".") {
        continue;
      }
      if (segment === "..") {
        return "";
      }
      segments.push(segment);
    }
    return segments.join("/");
  }

  // browse reads one directory of the open volume and reports whether the
  // listing was replaced. Every browse is a fresh read, so a deletion, a rename
  // or another window's change shows on the next one (FILE-C-3).
  async function browse(relative) {
    if (state.volume === null) {
      return false;
    }
    setStatus("正在读取目录…");
    let response;
    try {
      response = await read(entriesAddress(relative), {
        headers: { Authorization: `Bearer ${state.token}` },
        credentials: "omit",
      });
    } catch {
      setStatus("读取目录失败，请稍后重试", true);
      return false;
    }
    if (!response.ok) {
      showFailure(await failureOf(response), "读取目录失败");
      return false;
    }
    let answer;
    try {
      answer = await response.json();
    } catch {
      setStatus("读取目录失败：应答无法解析", true);
      return false;
    }
    const body = answer && typeof answer === "object" ? answer : {};
    state.path = relative;
    state.entries = Array.isArray(body.entries) ? body.entries.filter(isEntry) : [];
    state.limit = typeof body.limit === "number" ? body.limit : 0;
    state.total = typeof body.total === "number" ? body.total : state.entries.length;
    state.isTruncated = body.truncated === true;
    state.hasListing = true;
    state.preview = null;
    renderBrowser();
    setStatus("");
    return true;
  }

  function entriesAddress(relative) {
    const route = volumeRoute(state.volume) + "/entries";
    return relative === "" ? route : route + "?path=" + encodeURIComponent(relative);
  }

  function volumeRoute(volume) {
    return "/files/volumes/" + encodeURIComponent(volume.namespace) + "/" + encodeURIComponent(volume.name);
  }

  // openEntry descends into a directory and previews every other kind. A link
  // is not followed by the window: the data plane decides whether its target
  // stays inside the volume and answers the reason when it does not (FILE-C-3).
  function openEntry(entry) {
    if (entry.kind === KIND_DIRECTORY) {
      void browse(joinPath(state.path, entry.name));
      return;
    }
    void previewEntry(entry);
  }

  // previewEntry reads one file without changing it: the data plane opens it
  // read only, and the window sends nothing but the read (FILE-C-3).
  async function previewEntry(entry) {
    if (state.volume === null) {
      return;
    }
    setStatus("正在读取预览…");
    const relative = joinPath(state.path, entry.name);
    const address = volumeRoute(state.volume) + "/preview?path=" + encodeURIComponent(relative);
    let response;
    try {
      response = await read(address, {
        headers: { Authorization: `Bearer ${state.token}` },
        credentials: "omit",
      });
    } catch {
      setStatus("读取预览失败，请稍后重试", true);
      return;
    }
    if (!response.ok) {
      clearPreview();
      showFailure(await failureOf(response), "预览失败");
      return;
    }
    let answer;
    try {
      answer = await response.json();
    } catch {
      setStatus("读取预览失败：应答无法解析", true);
      return;
    }
    const body = answer && typeof answer === "object" ? answer : {};
    state.preview = {
      path: typeof body.path === "string" ? body.path : relative,
      isImage: body.kind === "image",
      contentType: typeof body.contentType === "string" ? body.contentType : "",
      size: typeof body.size === "number" ? body.size : 0,
      modifiedAt: typeof body.modifiedAt === "string" ? body.modifiedAt : "",
      content: typeof body.content === "string" ? body.content : "",
      isTruncated: body.truncated === true,
    };
    renderPreview();
    setStatus("");
  }

  // failureOf reads the refusal envelope the data plane answers, so the window
  // shows the service's own reason instead of a bare status code (FILE-C-3,
  // FILE-C-4).
  async function failureOf(response) {
    const body = await bodyOf(response);
    const error = body && typeof body === "object" && body.error && typeof body.error === "object" ? body.error : {};
    return {
      status: response.status,
      code: typeof error.code === "string" ? error.code : "",
      message: typeof error.message === "string" ? error.message : "",
      transfer: body && typeof body === "object" && body.transfer && typeof body.transfer === "object" ? body.transfer : null,
    };
  }

  // bodyOf reads one answer, so a caller keeps the status it can report even
  // when the answer is not JSON.
  async function bodyOf(response) {
    try {
      return await response.json();
    } catch {
      return null;
    }
  }

  // showFailure keeps the reason readable; a lost token is the one refusal that
  // ends this window (KOSD-C-75).
  function showFailure(failure, fallback) {
    if (TOKEN_CODES.has(failure.code)) {
      setStatus("窗口令牌已失效，请重新打开窗口", true);
      return;
    }
    setStatus(failure.message || failure.code || `${fallback}（${failure.status}）`, true);
  }

  // uploadFile opens one upload session for the chosen file and runs it to the
  // end. The session is opened without overwrite first: a same-name file comes
  // back as a conflict and waits for the reader's confirmation (FILE-C-4).
  async function uploadFile(file) {
    if (state.volume === null || !file || typeof file.name !== "string" || file.name === "") {
      return;
    }
    if (state.transfer !== null && !SETTLED_TRANSFER_STATES.has(state.transfer.state)) {
      setStatus("上一次传输还在进行，先结束再上传", true);
      return;
    }
    const source = { file, path: state.path, route: volumeRoute(state.volume) };
    state.upload = source;
    await openUpload(source, false);
  }

  // openUpload opens one session and, when the target is free, sends the whole
  // file through it. The content goes in parts, so the service's progress can
  // be read while the transfer runs (FILE-C-4).
  async function openUpload(source, overwrite) {
    const transfer = {
      direction: "upload",
      id: "",
      route: source.route,
      name: source.file.name,
      path: source.path,
      state: "opening",
      size: source.file.size,
      received: 0,
      reason: "",
      conflict: null,
      isCancelled: false,
    };
    state.transfer = transfer;
    renderTransfer();
    setStatus(`正在打开上传会话：${transfer.name}`);
    let response;
    try {
      response = await read(source.route + "/transfers", {
        method: "POST",
        headers: {
          Authorization: `Bearer ${state.token}`,
          "Content-Type": "application/json",
        },
        credentials: "omit",
        body: JSON.stringify({ path: source.path, name: transfer.name, size: source.file.size, overwrite }),
      });
    } catch {
      refuseTransfer(transfer, { status: 0, code: "", message: "上传失败：无法连接数据面" }, "打开上传会话");
      return;
    }
    if (!response.ok) {
      refuseTransfer(transfer, await failureOf(response), "打开上传会话");
      return;
    }
    adoptTransfer(transfer, await bodyOf(response));
    if (transfer.state === "conflict") {
      // Nothing has been sent and the target is untouched until the reader
      // confirms the replacement (FILE-C-4).
      setStatus(`目标已有同名文件，确认后才替换：${transfer.name}`, true);
      return;
    }
    if (transfer.state === "completed") {
      await refreshListing(transfer, `上传完成：${transfer.name}，共 ${byteLabel(transfer.size)}`);
      return;
    }
    await sendContent(transfer, source);
  }

  // sendContent sends the source in parts: every answer carries how much the
  // service holds, and the progress read between parts is the service's own
  // count rather than the window's guess (FILE-C-4).
  async function sendContent(transfer, source) {
    transfer.state = "uploading";
    renderTransfer();
    setStatus(`正在上传：${transfer.name}`);
    for (let offset = 0; offset < source.file.size; offset += UPLOAD_CHUNK_BYTES) {
      if (transfer.isCancelled) {
        return;
      }
      const end = Math.min(offset + UPLOAD_CHUNK_BYTES, source.file.size);
      const chunk = await source.file.slice(offset, end).arrayBuffer();
      const answered = await putContent(transfer, chunk);
      if (!answered || transfer.isCancelled) {
        return;
      }
      if (transfer.state === "completed") {
        await refreshListing(transfer, `上传完成：${transfer.name}，共 ${byteLabel(transfer.received)}`);
        return;
      }
      await readProgress(transfer);
    }
    // Every byte was sent but the service did not report the file whole, so the
    // target is not claimed as written (FILE-C-4).
    refuseTransfer(transfer, { status: 0, code: "", message: "上传未完成：数据面没有确认收全" }, "上传");
  }

  // putContent appends one part and reports whether the session still moves. A
  // same-name file that appeared during the transfer turns the session into a
  // conflict that waits for confirmation (FILE-C-4).
  async function putContent(transfer, chunk) {
    let response;
    try {
      response = await read(transfer.route + "/transfers/" + encodeURIComponent(transfer.id) + "/content", {
        method: "PUT",
        headers: {
          Authorization: `Bearer ${state.token}`,
          "Content-Type": "application/octet-stream",
        },
        credentials: "omit",
        body: chunk,
      });
    } catch {
      if (!transfer.isCancelled) {
        refuseTransfer(transfer, { status: 0, code: "", message: "上传中断：无法连接数据面" }, "上传");
      }
      return false;
    }
    if (transfer.isCancelled) {
      return false;
    }
    if (!response.ok) {
      const failure = await failureOf(response);
      if (failure.code === "upload_conflict") {
        adoptTransfer(transfer, failure.transfer);
        // The service dropped the staged bytes with the refusal, so the resend
        // the confirmation leads to starts from zero.
        transfer.received = 0;
        renderTransfer();
        setStatus(failure.message, true);
        return false;
      }
      refuseTransfer(transfer, failure, "上传");
      return false;
    }
    adoptTransfer(transfer, await bodyOf(response));
    return true;
  }

  // readProgress reads between parts, so the numbers on screen come from the
  // service that holds the bytes (FILE-C-4).
  async function readProgress(transfer) {
    let response;
    try {
      response = await read(transfer.route + "/transfers/" + encodeURIComponent(transfer.id), {
        headers: { Authorization: `Bearer ${state.token}` },
        credentials: "omit",
      });
    } catch {
      return;
    }
    if (!response.ok) {
      return;
    }
    adoptTransfer(transfer, await bodyOf(response));
  }

  // adoptTransfer copies one session answer onto the surface. Absent fields
  // keep their last value, so a progress read never erases the declared size.
  function adoptTransfer(transfer, answer) {
    if (!answer || typeof answer !== "object") {
      return;
    }
    if (typeof answer.id === "string") {
      transfer.id = answer.id;
    }
    if (typeof answer.state === "string") {
      transfer.state = answer.state;
    }
    if (typeof answer.size === "number") {
      transfer.size = answer.size;
    }
    if (typeof answer.received === "number") {
      transfer.received = answer.received;
    }
    if (typeof answer.reason === "string" && answer.reason !== "") {
      transfer.reason = answer.reason;
    }
    if (answer.conflict && typeof answer.conflict === "object") {
      transfer.conflict = {
        size: typeof answer.conflict.size === "number" ? answer.conflict.size : 0,
        modifiedAt: typeof answer.conflict.modifiedAt === "string" ? answer.conflict.modifiedAt : "",
      };
    }
    renderTransfer();
  }

  // refuseTransfer ends one transfer with the service's own reason readable; a
  // lost token is the one refusal that ends this window (FILE-C-4, KOSD-C-75).
  function refuseTransfer(transfer, failure, fallback) {
    const reason = failure.message || failure.code || `${fallback}（${failure.status}）`;
    transfer.state = "failed";
    transfer.reason = reason;
    renderTransfer();
    if (TOKEN_CODES.has(failure.code)) {
      setStatus("窗口令牌已失效，请重新打开窗口", true);
      return;
    }
    setStatus(reason, true);
  }

  // confirmOverwrite re-opens the session with the reader's confirmation, which
  // is what lets the service replace the same-name file (FILE-C-4).
  async function confirmOverwrite() {
    const transfer = state.transfer;
    const source = state.upload;
    if (transfer === null || transfer.state !== "conflict" || source === null) {
      return;
    }
    await openUpload(source, true);
  }

  // cancelTransfer stops the running upload with DELETE: the service removes
  // the staged bytes and leaves the target as it was (FILE-C-4).
  async function cancelTransfer() {
    const transfer = state.transfer;
    if (transfer === null || transfer.direction !== "upload" || SETTLED_TRANSFER_STATES.has(transfer.state)) {
      return;
    }
    transfer.isCancelled = true;
    if (transfer.id === "") {
      transfer.state = "cancelled";
      renderTransfer();
      setStatus(`已取消上传：${transfer.name}`);
      return;
    }
    setStatus(`正在取消上传：${transfer.name}`);
    let response;
    try {
      response = await read(transfer.route + "/transfers/" + encodeURIComponent(transfer.id), {
        method: "DELETE",
        headers: { Authorization: `Bearer ${state.token}` },
        credentials: "omit",
      });
    } catch {
      refuseTransfer(transfer, { status: 0, code: "", message: "取消失败：无法连接数据面" }, "取消上传");
      return;
    }
    if (!response.ok) {
      refuseTransfer(transfer, await failureOf(response), "取消上传");
      return;
    }
    adoptTransfer(transfer, await bodyOf(response));
    await refreshListing(transfer, `已取消上传：${transfer.name}，目标位置未改变`);
  }

  function dismissTransfer() {
    state.transfer = null;
    state.upload = null;
    renderTransfer();
  }

  // refreshListing reads the directory again after a transfer settled, so the
  // surface shows what is there rather than what the transfer claimed
  // (FILE-C-4).
  async function refreshListing(transfer, message) {
    const isBrowsing =
      state.volume !== null && volumeRoute(state.volume) === transfer.route && state.path === transfer.path;
    if (!isBrowsing) {
      setStatus(message);
      return;
    }
    if (await browse(state.path)) {
      setStatus(message);
    }
  }

  // downloadEntry streams one file so the window can show how much of the
  // declared size has arrived, then hands the whole body to the reader's disk
  // (FILE-C-4).
  async function downloadEntry(entry) {
    if (state.volume === null) {
      return;
    }
    const relative = joinPath(state.path, entry.name);
    const transfer = {
      direction: "download",
      id: "",
      route: volumeRoute(state.volume),
      name: entry.name,
      path: state.path,
      state: "downloading",
      size: typeof entry.size === "number" ? entry.size : 0,
      received: 0,
      reason: "",
      conflict: null,
      isCancelled: false,
    };
    state.transfer = transfer;
    renderTransfer();
    setStatus(`正在下载：${transfer.name}`);
    let response;
    try {
      response = await read(transfer.route + "/download?path=" + encodeURIComponent(relative), {
        headers: { Authorization: `Bearer ${state.token}` },
        credentials: "omit",
      });
    } catch {
      refuseTransfer(transfer, { status: 0, code: "", message: "下载失败：无法连接数据面" }, "下载");
      return;
    }
    if (!response.ok) {
      refuseTransfer(transfer, await failureOf(response), "下载");
      return;
    }
    const declared = Number(response.headers.get("content-length"));
    if (Number.isFinite(declared) && declared >= 0) {
      transfer.size = declared;
    }
    renderTransfer();
    const reader = response.body.getReader();
    const parts = [];
    for (;;) {
      const { value, done } = await reader.read();
      if (done) {
        break;
      }
      parts.push(value);
      transfer.received += value.byteLength;
      renderTransfer();
    }
    if (transfer.size === 0) {
      transfer.size = transfer.received;
    }
    if (transfer.received !== transfer.size) {
      refuseTransfer(
        transfer,
        { status: 0, code: "", message: `下载中断：收到 ${byteLabel(transfer.received)}，共 ${byteLabel(transfer.size)}` },
        "下载",
      );
      return;
    }
    transfer.state = "completed";
    renderTransfer();
    persist(transfer.name, joinChunks(parts), response.headers.get("content-type") || "");
    setStatus(`下载完成：${transfer.name}，共 ${byteLabel(transfer.received)}`);
  }

  // renderTransfer lays out the running transfer: what is moving, the state the
  // service reports, how much of the declared size arrived, and the actions
  // that apply to that state (FILE-C-4).
  function renderTransfer() {
    const transfer = state.transfer;
    elements.transferPanel.hidden = transfer === null;
    if (transfer === null) {
      elements.transferName.textContent = "";
      elements.transferState.textContent = "";
      elements.transferMeta.textContent = "";
      elements.transferProgress.max = 1;
      elements.transferProgress.value = 0;
      elements.transferConfirm.hidden = true;
      elements.transferCancel.hidden = true;
      elements.transferDismiss.hidden = true;
      return;
    }
    elements.transferState.textContent = TRANSFER_STATE_LABELS[transfer.state] || "进行中";
    elements.transferName.textContent = `${transfer.direction === "upload" ? "上传" : "下载"} ${transfer.name}`;
    const hasTotal = transfer.size > 0;
    elements.transferProgress.max = hasTotal ? transfer.size : 1;
    elements.transferProgress.value = hasTotal ? Math.min(transfer.received, transfer.size) : 0;
    const parts = [];
    if (hasTotal) {
      parts.push(`${percentLabel(transfer.received, transfer.size)}%（${byteLabel(transfer.received)} / ${byteLabel(transfer.size)}）`);
    } else {
      parts.push(byteLabel(transfer.received));
    }
    if (transfer.conflict !== null) {
      parts.push(`已有同名文件 ${byteLabel(transfer.conflict.size)} · ${instantLabel(transfer.conflict.modifiedAt)}`);
    }
    if (transfer.reason !== "") {
      parts.push(transfer.reason);
    }
    elements.transferMeta.textContent = parts.join(" · ");
    elements.transferConfirm.hidden = transfer.state !== "conflict";
    elements.transferCancel.hidden =
      transfer.direction !== "upload" || SETTLED_TRANSFER_STATES.has(transfer.state) || transfer.state === "opening";
    elements.transferDismiss.hidden = !SETTLED_TRANSFER_STATES.has(transfer.state);
  }

  // leaveVolume returns to the volume list and drops the directory, the listing
  // and any preview of the closed volume.
  function leaveVolume() {
    state.volume = null;
    state.path = "";
    state.entries = [];
    state.limit = 0;
    state.total = 0;
    state.isTruncated = false;
    state.hasListing = false;
    state.preview = null;
    state.transfer = null;
    state.upload = null;
    renderBrowser();
    setStatus(`${state.volumes.length} 个授权卷`);
  }

  // renderBrowser lays out the open volume: the location, the entries the data
  // plane served in name order, what the bound left out, the preview, and the
  // transfer that is running for it.
  function renderBrowser() {
    const isOpen = state.volume !== null;
    elements.browser.hidden = !isOpen;
    elements.back.hidden = !isOpen;
    elements.root.hidden = !isOpen;
    elements.refresh.hidden = !isOpen;
    elements.upload.hidden = !isOpen;
    renderPath();
    elements.entries.replaceChildren();
    for (const entry of state.entries) {
      elements.entries.append(entryNode(entry));
    }
    elements.entries.className = "entries " + state.view;
    if (state.hasListing && state.entries.length === 0) {
      elements.entriesEmpty.textContent = "此目录为空。";
      elements.entriesEmpty.hidden = false;
    } else if (!state.hasListing) {
      elements.entriesEmpty.textContent = "目录未读取。";
      elements.entriesEmpty.hidden = false;
    } else {
      elements.entriesEmpty.hidden = true;
    }
    elements.entriesCount.textContent = state.hasListing ? `${state.entries.length} 项` : "";
    elements.truncated.hidden = !state.isTruncated;
    elements.truncated.textContent = state.isTruncated
      ? `已截断：显示前 ${state.limit} 项，共 ${state.total} 项`
      : "";
    renderPreview();
    renderTransfer();
  }

  function entryNode(entry) {
    const item = doc.createElement("li");
    item.className = "entry";
    item.dataset.kind = entry.kind;

    const open = doc.createElement("button");
    open.type = "button";
    open.className = "entry-name";
    open.textContent = entry.name;
    open.dataset.entry = entry.name;
    open.dataset.kind = entry.kind;
    open.addEventListener("click", () => openEntry(entry));

    const kind = doc.createElement("span");
    kind.className = "entry-kind";
    kind.textContent = KIND_LABELS[entry.kind] || "未知类型";

    const size = doc.createElement("span");
    size.className = "entry-size";
    size.textContent = byteLabel(entry.size);

    const modified = doc.createElement("span");
    modified.className = "entry-modified";
    modified.textContent = instantLabel(entry.modifiedAt);

    item.append(open, kind, size, modified);

    // Only a regular file can be downloaded: the data plane refuses anything
    // else with its reason (FILE-C-4).
    if (entry.kind === KIND_FILE) {
      const download = doc.createElement("button");
      download.type = "button";
      download.className = "entry-download";
      download.textContent = "下载";
      download.dataset.entry = entry.name;
      download.addEventListener("click", () => void downloadEntry(entry));
      item.append(download);
    }
    return item;
  }

  // clearPreview closes the preview surface so a refusal never leaves a blank
  // panel or a stale file on screen (FILE-C-3).
  function clearPreview() {
    state.preview = null;
    renderPreview();
  }

  function renderPreview() {
    const preview = state.preview;
    elements.previewPanel.hidden = preview === null;
    if (preview === null) {
      elements.previewText.textContent = "";
      elements.previewText.hidden = true;
      elements.previewImage.removeAttribute("src");
      elements.previewImage.alt = "";
      elements.previewImage.hidden = true;
      elements.previewMeta.textContent = "";
      elements.previewCut.hidden = true;
      return;
    }
    elements.previewText.hidden = preview.isImage;
    elements.previewImage.hidden = !preview.isImage;
    if (preview.isImage) {
      elements.previewText.textContent = "";
      elements.previewImage.setAttribute("src", preview.content);
      elements.previewImage.alt = preview.path;
    } else {
      elements.previewImage.removeAttribute("src");
      elements.previewImage.alt = "";
      elements.previewText.textContent = preview.content;
    }
    elements.previewCut.hidden = !preview.isTruncated;
    elements.previewMeta.textContent = [
      preview.path,
      preview.contentType,
      byteLabel(preview.size),
      instantLabel(preview.modifiedAt),
    ]
      .filter((part) => part !== "")
      .join(" · ");
  }

  function setStatus(message, isBlocked = false) {
    elements.status.textContent = message;
    elements.status.classList.toggle("blocked", isBlocked);
  }

  return { start };
}

// isVolumeEntry keeps a malformed entry out of the list instead of rendering a
// row that names nothing.
function isVolumeEntry(entry) {
  return entry !== null && typeof entry === "object" && typeof entry.name === "string" && entry.name !== "";
}

// isEntry keeps a malformed directory entry out of the listing instead of
// rendering a row that names nothing.
function isEntry(entry) {
  return entry !== null && typeof entry === "object" && typeof entry.name === "string" && entry.name !== "";
}

// joinPath appends one entry name to a relative directory path.
function joinPath(base, name) {
  return base === "" ? name : base + "/" + name;
}

// percentLabel reads how much of one declared total has arrived, capped so a
// report the window cannot explain never reads above the whole.
function percentLabel(received, total) {
  if (total <= 0) {
    return "100";
  }
  return String(Math.min(100, Math.floor((received / total) * 100)));
}

// joinChunks copies the received parts into one buffer for the reader's disk.
function joinChunks(parts) {
  const size = parts.reduce((sum, part) => sum + part.byteLength, 0);
  const joined = new Uint8Array(size);
  let offset = 0;
  for (const part of parts) {
    joined.set(part, offset);
    offset += part.byteLength;
  }
  return joined;
}

// saveDownloadedFile hands one finished download to the browser's own save
// path through an object address on an anchor the document does not show.
function saveDownloadedFile(doc, name, bytes, contentType) {
  const address = URL.createObjectURL(new Blob([bytes], { type: contentType || "application/octet-stream" }));
  const link = doc.createElement("a");
  link.href = address;
  link.download = name;
  link.click();
  // The address stays valid until the browser has started the save.
  setTimeout(() => URL.revokeObjectURL(address), 60 * 1000);
}

// byteLabel renders one entry size with its unit; a size the answer did not
// carry shows nothing rather than a misleading number.
function byteLabel(value) {
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) {
    return "";
  }
  let size = value;
  let unit = 0;
  while (size >= BYTE_STEP && unit < BYTE_UNITS.length - 1) {
    size /= BYTE_STEP;
    unit += 1;
  }
  return `${unit === 0 ? String(size) : size.toFixed(1)} ${BYTE_UNITS[unit]}`;
}

// instantLabel renders one RFC 3339 instant in UTC, so the same answer reads
// the same in every browser timezone.
function instantLabel(value) {
  if (typeof value !== "string" || value === "") {
    return "";
  }
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return value;
  }
  return parsed.toISOString().slice(0, 16).replace("T", " ") + " UTC";
}

// addressSearch reads the query of one frame address; an address that does not
// parse names no console.
function addressSearch(href) {
  try {
    return new URL(href).searchParams;
  } catch {
    return null;
  }
}

// originOf returns the origin of one absolute address, or "" when the value is
// not one; the handshake compares whole origins, never path prefixes.
function originOf(value) {
  try {
    const parsed = new URL(value);
    return parsed.origin === "null" ? "" : parsed.origin;
  } catch {
    return "";
  }
}

if (typeof window !== "undefined" && typeof document !== "undefined") {
  createFileManager({ document, window }).start();
}
