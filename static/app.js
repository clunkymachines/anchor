const telemetryRootID = "device-telemetry";
const rememberedTabsByRootID = new Map();

function closestEventTarget(event, selector) {
  if (!(event.target instanceof Element)) {
    return null;
  }
  return event.target.closest(selector);
}

function all(selector, root = document) {
  return Array.from(root.querySelectorAll(selector));
}

// Organisation picker

function closeOrganisationPickers() {
  all("[data-organisation-picker-button]").forEach((button) => {
    button.setAttribute("aria-expanded", "false");
    button.closest("[data-organisation-picker]")?.querySelector(".organisation-menu")?.setAttribute("hidden", "");
  });
}

function handleOrganisationPickerClick(event) {
  const button = closestEventTarget(event, "[data-organisation-picker-button]");
  if (!button) {
    return false;
  }

  const menu = button.closest("[data-organisation-picker]")?.querySelector(".organisation-menu");
  if (!menu) {
    return true;
  }

  const wasOpen = button.getAttribute("aria-expanded") === "true";
  closeOrganisationPickers();

  if (!wasOpen) {
    button.setAttribute("aria-expanded", "true");
    menu.removeAttribute("hidden");
  }
  return true;
}

function closeOrganisationPickersOnOutsideClick(event) {
  if (!closestEventTarget(event, "[data-organisation-picker]")) {
    closeOrganisationPickers();
  }
}

// Password visibility

function handlePasswordToggleClick(event) {
  const toggle = closestEventTarget(event, "[data-password-toggle]");
  if (!toggle) {
    return false;
  }

  const input = toggle.closest(".password-input")?.querySelector('input[type="password"], input[type="text"]');
  if (!input) {
    return true;
  }

  const shouldShowPassword = input.type === "password";
  input.type = shouldShowPassword ? "text" : "password";
  toggle.classList.toggle("is-visible", shouldShowPassword);
  toggle.setAttribute("aria-pressed", String(shouldShowPassword));
  toggle.setAttribute("aria-label", shouldShowPassword ? "Hide password" : "Show password");
  return true;
}

// Tabs

function activateTab(group, tabName) {
  all("[data-tab-button]", group).forEach((button) => {
    const isActive = button.dataset.tabButton === tabName;
    button.classList.toggle("is-active", isActive);
    button.setAttribute("aria-selected", String(isActive));
  });

  all("[data-tab-panel]", group).forEach((panel) => {
    panel.classList.toggle("is-hidden", panel.dataset.tabPanel !== tabName);
  });
}

function activeTabName(root) {
  return root.querySelector("[data-tab-button].is-active")?.dataset.tabButton || "";
}

function tabGroupHasTab(group, tabName) {
  return all("[data-tab-button]", group).some((button) => button.dataset.tabButton === tabName);
}

function handleTabClick(event) {
  const button = closestEventTarget(event, "[data-tab-button]");
  if (!button) {
    return false;
  }

  const group = button.closest("[data-tab-group]");
  if (group) {
    activateTab(group, button.dataset.tabButton);
  }
  return true;
}

// Local timestamps

const localDateTimeFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "medium",
});

function formatLocalTimestamp(value) {
  if (!value) {
    return "";
  }

  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  return localDateTimeFormatter.format(date);
}

function formatLocalTimes(root = document) {
  all("[data-local-time]", root).forEach((element) => {
    const value = element.getAttribute("datetime") || element.dataset.localTime;
    const formatted = formatLocalTimestamp(value);
    if (formatted) {
      element.textContent = formatted;
      element.setAttribute("title", value);
    }
  });

  all("[data-local-time-title]", root).forEach((element) => {
    const formatted = formatLocalTimestamp(element.dataset.localTimeTitle);
    if (formatted) {
      element.setAttribute("title", `${element.dataset.localTimeTitlePrefix || ""}${formatted}`);
    }
  });
}

// Server-sent events

function dispatchBodyEvent(name) {
  document.body.dispatchEvent(new Event(name, { bubbles: true }));
}

function startDeviceEventStream(deviceEvents) {
  const stream = new EventSource(deviceEvents.dataset.deviceEventsUrl);
  stream.addEventListener("device-telemetry", () => {
    dispatchBodyEvent("device-telemetry-refresh");
  });
  stream.addEventListener("device-tasks", () => {
    dispatchBodyEvent("device-tasks-refresh");
  });
  return stream;
}

function releaseCVEStateRefresher() {
  let refreshInFlight = false;
  let refreshQueued = false;

  return async function refreshReleaseCVEState() {
    const releaseState = document.querySelector("[data-release-cves-url]");
    if (!releaseState) {
      return;
    }

    if (refreshInFlight) {
      refreshQueued = true;
      return;
    }

    refreshInFlight = true;
    try {
      const response = await fetch(releaseState.dataset.releaseCvesUrl, {
        credentials: "same-origin",
        headers: { "X-Requested-With": "fetch" },
      });
      if (!response.ok) {
        return;
      }

      releaseState.outerHTML = await response.text();
      formatLocalTimes(document);
      window.htmx?.process(document.body);
    } finally {
      refreshInFlight = false;
      if (refreshQueued) {
        refreshQueued = false;
        refreshReleaseCVEState();
      }
    }
  };
}

function startReleaseEventStream(releaseEvents) {
  const stream = new EventSource(releaseEvents.dataset.releaseEventsUrl);
  const refreshReleaseCVEState = releaseCVEStateRefresher();

  stream.addEventListener("open", refreshReleaseCVEState);
  stream.addEventListener("release-cves", refreshReleaseCVEState);
  return stream;
}

function startEventStreams() {
  if (!window.EventSource) {
    return [];
  }

  const streams = [];
  const deviceEvents = document.querySelector("[data-device-events-url]");
  const releaseEvents = document.querySelector("[data-release-events-url]");

  if (deviceEvents) {
    streams.push(startDeviceEventStream(deviceEvents));
  }
  if (releaseEvents) {
    streams.push(startReleaseEventStream(releaseEvents));
  }
  return streams;
}

// HTMX swaps

function rememberTelemetryTabBeforeSwap(event) {
  const target = event.detail?.target;
  if (!(target instanceof Element) || target.id !== telemetryRootID) {
    return;
  }

  const tabName = activeTabName(target);
  if (tabName) {
    rememberedTabsByRootID.set(target.id, tabName);
  }
}

function restoreRememberedTabs(root) {
  if (!(root instanceof Element)) {
    return;
  }

  const tabName = rememberedTabsByRootID.get(root.id);
  if (!tabName) {
    return;
  }

  all("[data-tab-group]", root).forEach((group) => {
    if (tabGroupHasTab(group, tabName)) {
      activateTab(group, tabName);
    }
  });
  rememberedTabsByRootID.delete(root.id);
}

function restoreRememberedTabsAfterSettle(event) {
  restoreRememberedTabs(event.target);

  rememberedTabsByRootID.forEach((_, rootID) => {
    restoreRememberedTabs(document.getElementById(rootID));
  });
}

// Event wiring

document.addEventListener("click", (event) => {
  if (handleOrganisationPickerClick(event)) {
    return;
  }

  closeOrganisationPickersOnOutsideClick(event);

  if (handlePasswordToggleClick(event)) {
    return;
  }
  handleTabClick(event);
});

document.addEventListener("keydown", (event) => {
  if (event.key === "Escape") {
    closeOrganisationPickers();
  }
});

document.addEventListener("DOMContentLoaded", () => {
  formatLocalTimes();

  const eventStreams = startEventStreams();
  window.addEventListener("beforeunload", () => {
    eventStreams.forEach((stream) => stream.close());
  });
});

document.body.addEventListener("htmx:beforeSwap", rememberTelemetryTabBeforeSwap);

document.body.addEventListener("htmx:afterSwap", (event) => {
  formatLocalTimes(event.target);
});

document.body.addEventListener("htmx:afterSettle", restoreRememberedTabsAfterSettle);
