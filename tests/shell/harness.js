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
    addEventListener(name, handler) {
      const handlers = element.listeners.get(name) || [];
      handlers.push(handler);
      element.listeners.set(name, handlers);
    },
    click() {
      for (const handler of element.listeners.get("click") || []) handler({ target: element });
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

// fakeFetch answers by "METHOD URL" and records what the window sent.
export function fakeFetch(routes) {
  const calls = [];
  const fetch = async (url, init = {}) => {
    const call = {
      method: init.method || "GET",
      url,
      headers: init.headers || {},
      credentials: init.credentials,
    };
    calls.push(call);
    const route = routes[`${call.method} ${url}`];
    if (!route) {
      return reply(404, { error: { code: "not_found", message: "missing", stage: "read" } });
    }
    return reply(route.status, route.body);
  };
  return { fetch, calls };
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
