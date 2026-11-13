// Shared browser stand-ins for the file-manager window: the document the view
// builds with, a frame window that records what the application posts and
// delivers console messages, a fetch that answers by route, and the interval
// clock the liveness report runs on. The window injects every one of them, so
// the cases exercise the module the browser runs.

// fakeDocument builds the document the window reads its ids from.
export function fakeDocument(ids = {}) {
  return {
    documentElement: fakeElement("html"),
    createElement: (tag) => fakeElement(tag),
    createTextNode: (value) => ({ nodeType: 3, textContent: String(value) }),
    getElementById: (id) => ids[id] || null,
  };
}

// windowDocument builds the document the packaged entry renders, so every case
// drives the ids the shipped document actually carries.
export function windowDocument() {
  const ids = [
    "status",
    "path",
    "volumes",
    "volumes-empty",
    "count",
    "problems",
    "view-list",
    "view-grid",
    "browser-panel",
    "entries",
    "entries-empty",
    "entries-count",
    "truncated",
    "refresh",
    "back",
    "root",
    "preview-panel",
    "preview-meta",
    "preview-text",
    "preview-image",
    "preview-cut",
    "preview-close",
    "upload",
    "upload-file",
    "transfer-panel",
    "transfer-name",
    "transfer-state",
    "transfer-progress",
    "transfer-meta",
    "transfer-confirm",
    "transfer-cancel",
    "transfer-dismiss",
  ];
  const elements = {};
  for (const id of ids) elements[id] = fakeElement(id);
  return fakeDocument(elements);
}

// fakeElement is the element API the view builds nodes with. textContent reads
// the own text and every descendant's, which is what a rendered document shows.
export function fakeElement(tag) {
  let ownText = "";
  const classes = new Set();
  const element = {
    tagName: String(tag).toUpperCase(),
    nodeType: 1,
    children: [],
    hidden: false,
    type: "",
    dataset: {},
    attributes: new Map(),
    listeners: new Map(),
    className: "",
    classList: {
      add(name) {
        classes.add(name);
        element.className = [...classes].join(" ");
      },
      contains(name) {
        return classes.has(name);
      },
      toggle(name, force) {
        const on = force === undefined ? !classes.has(name) : Boolean(force);
        if (on) element.classList.add(name);
        else {
          classes.delete(name);
          element.className = [...classes].join(" ");
        }
        return on;
      },
    },
    append(...nodes) {
      for (const node of nodes) {
        node.parentNode = element;
        element.children.push(node);
      }
    },
    appendChild(node) {
      element.append(node);
      return node;
    },
    replaceChildren(...nodes) {
      element.children = [];
      element.append(...nodes);
    },
    setAttribute(name, value) {
      element.attributes.set(String(name), String(value));
    },
    getAttribute(name) {
      return element.attributes.has(String(name)) ? element.attributes.get(String(name)) : null;
    },
    removeAttribute(name) {
      element.attributes.delete(String(name));
    },
    addEventListener(name, handler) {
      const handlers = element.listeners.get(name) || [];
      handlers.push(handler);
      element.listeners.set(name, handlers);
    },
    // dispatch delivers one event to the handlers the view registered, which
    // is how a case drives a control whose browser gesture it cannot perform.
    dispatch(name, event = {}) {
      for (const handler of element.listeners.get(name) || []) handler({ target: element, ...event });
    },
    click() {
      element.dispatch("click");
    },
  };
  Object.defineProperty(element, "textContent", {
    get() {
      if (element.children.length === 0) return ownText;
      return ownText + element.children.map((child) => child.textContent).join("");
    },
    set(value) {
      ownText = value === undefined || value === null ? "" : String(value);
      element.children = [];
    },
  });
  return element;
}

// findByClass returns the first element below root carrying one class name, so
// a case clicks the control the view built instead of knowing its tag order.
export function findByClass(root, name) {
  if (!root || !Array.isArray(root.children)) return null;
  for (const child of root.children) {
    if (typeof child.className === "string" && child.className.split(/\s+/).includes(name)) return child;
    const found = findByClass(child, name);
    if (found) return found;
  }
  return null;
}

// fakeWindow is the frame's window: its location carries the address the host
// loaded it from, parent.postMessage records every report, and deliver hands a
// console message to the registered listener.
export function fakeWindow(address) {
  const posted = [];
  const listeners = new Map();
  const parent = {
    postMessage(data, targetOrigin) {
      posted.push({ data, targetOrigin });
    },
  };
  return {
    location: { href: address, ancestorOrigins: [] },
    parent,
    posted,
    addEventListener(name, handler) {
      const handlers = listeners.get(name) || [];
      handlers.push(handler);
      listeners.set(name, handlers);
    },
    deliver(event) {
      for (const handler of listeners.get("message") || []) handler(event);
    },
  };
}

// reply builds the one response shape the window reads.
export function reply(status, body) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  };
}

// fakeFetch answers by "METHOD URL" and records what the window sent. A route
// is either the {status, body} a reply is built from, a ready response (as
// streamReply returns), or a function of the recorded call for answers that
// differ per request.
export function fakeFetch(routes) {
  const calls = [];
  const fetch = async (url, init = {}) => {
    const call = {
      method: init.method || "GET",
      url,
      headers: init.headers || {},
      credentials: init.credentials,
      body: init.body,
    };
    calls.push(call);
    let route = routes[`${call.method} ${url}`];
    if (typeof route === "function") route = await route(call);
    if (!route) {
      return reply(404, { error: { code: "not_found", message: "missing", stage: "read" } });
    }
    if (typeof route.ok === "boolean") return route;
    return reply(route.status, route.body);
  };
  return { fetch, calls };
}

// deferred hands a promise to the case and its resolution to the case too, so
// an answer can be held while the window is mid-transfer.
export function deferred() {
  let resolve;
  const promise = new Promise((settle) => {
    resolve = settle;
  });
  return { promise, resolve };
}

// streamReply answers with a body the window reads chunk by chunk, declaring
// the whole size the way the download route does. gates[i] holds chunk i back
// until the case resolves it, so progress can be observed mid-transfer.
export function streamReply(status, { total, chunks, gates = [], headers = {} }) {
  const entries = new Map(
    Object.entries({ "content-type": "application/octet-stream", ...headers }).map(([name, value]) => [
      name.toLowerCase(),
      String(value),
    ]),
  );
  entries.set("content-length", String(total));
  const body = new ReadableStream({
    async start(controller) {
      for (let index = 0; index < chunks.length; index += 1) {
        if (gates[index]) await gates[index];
        controller.enqueue(chunks[index]);
      }
      controller.close();
    },
  });
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: { get: (name) => (entries.has(String(name).toLowerCase()) ? entries.get(String(name).toLowerCase()) : null) },
    body,
  };
}

// fakeIntervals is the timer the liveness report runs on: nothing fires until a
// case fires it.
export function fakeIntervals() {
  const armed = [];
  return {
    setInterval(handler, milliseconds) {
      armed.push({ handler, milliseconds, cleared: false });
      return armed.length;
    },
    clearInterval(id) {
      const slot = armed[id - 1];
      if (slot) slot.cleared = true;
    },
    active() {
      return armed.filter((slot) => !slot.cleared);
    },
    fire() {
      for (const slot of armed) {
        if (!slot.cleared) slot.handler();
      }
    },
  };
}

// settle lets the window's read chain finish before a case asserts on it.
export async function settle() {
  await new Promise((resolve) => setTimeout(resolve, 5));
}
