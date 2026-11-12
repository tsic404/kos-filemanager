// File-manager window: the application side of the handshake that delivers the
// window's application token, and the authorized volume list the data plane
// serves together with the account's list/grid view and initial path
// (FILE-C-1, DASH-C-27, KOSD-C-75).
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

// createFileManager binds the window to one document, one frame window and one
// fetch. Every dependency is injected so the handshake, the read and both views
// can be exercised without a browser.
export function createFileManager({
  document: doc,
  window: frame,
  fetch: read = globalThis.fetch,
  setInterval: startInterval = globalThis.setInterval,
}) {
  const state = {
    consoleOrigin: "",
    windowId: "",
    token: "",
    view: VIEW_LIST,
    initialPath: "",
    volumes: [],
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
    setView(VIEW_LIST);
    renderVolumes();
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
    renderPath();
    renderVolumes();
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
    state.volumes = Array.isArray(body.volumes) ? body.volumes.filter(isVolumeEntry) : [];
    state.problems = Array.isArray(body.problems) ? body.problems.filter((entry) => typeof entry === "string") : [];
  }

  // setView switches the one volume list between the two declared views. It is
  // presentation state: no preference is written from here.
  function setView(view) {
    state.view = view === VIEW_GRID ? VIEW_GRID : VIEW_LIST;
    elements.volumes.className = "volumes " + state.view;
    elements.viewList.setAttribute("aria-pressed", String(state.view === VIEW_LIST));
    elements.viewGrid.setAttribute("aria-pressed", String(state.view === VIEW_GRID));
  }

  function renderPath() {
    elements.path.textContent = state.initialPath || PATH_ROOT_LABEL;
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
    name.textContent = entry.name || "未命名卷";
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
