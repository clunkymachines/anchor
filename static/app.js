document.addEventListener("click", (event) => {
  const organisationPickerButton = event.target.closest("[data-organisation-picker-button]");
  if (organisationPickerButton) {
    const picker = organisationPickerButton.closest("[data-organisation-picker]");
    const menu = picker?.querySelector(".organisation-menu");
    if (!menu) {
      return;
    }

    const isOpen = organisationPickerButton.getAttribute("aria-expanded") === "true";
    document.querySelectorAll("[data-organisation-picker-button]").forEach((button) => {
      button.setAttribute("aria-expanded", "false");
      button.closest("[data-organisation-picker]")?.querySelector(".organisation-menu")?.setAttribute("hidden", "");
    });

    if (!isOpen) {
      organisationPickerButton.setAttribute("aria-expanded", "true");
      menu.removeAttribute("hidden");
    }
    return;
  }

  if (!event.target.closest("[data-organisation-picker]")) {
    document.querySelectorAll("[data-organisation-picker-button]").forEach((button) => {
      button.setAttribute("aria-expanded", "false");
      button.closest("[data-organisation-picker]")?.querySelector(".organisation-menu")?.setAttribute("hidden", "");
    });
  }

  const toggle = event.target.closest("[data-password-toggle]");
  if (toggle) {
    const field = toggle.closest(".password-input");
    const input = field?.querySelector('input[type="password"], input[type="text"]');
    if (!input) {
      return;
    }

    const showPassword = input.type === "password";
    input.type = showPassword ? "text" : "password";
    toggle.classList.toggle("is-visible", showPassword);
    toggle.setAttribute("aria-pressed", String(showPassword));
    toggle.setAttribute("aria-label", showPassword ? "Hide password" : "Show password");
    return;
  }

  const tabButton = event.target.closest("[data-tab-button]");
  if (tabButton) {
    const group = tabButton.closest("[data-tab-group]");
    if (!group) {
      return;
    }

    const tab = tabButton.dataset.tabButton;
    group.querySelectorAll("[data-tab-button]").forEach((button) => {
      const isActive = button === tabButton;
      button.classList.toggle("is-active", isActive);
      button.setAttribute("aria-selected", String(isActive));
    });
    group.querySelectorAll("[data-tab-panel]").forEach((panel) => {
      panel.classList.toggle("is-hidden", panel.dataset.tabPanel !== tab);
    });
    return;
  }

});

document.addEventListener("keydown", (event) => {
  if (event.key !== "Escape") {
    return;
  }

  document.querySelectorAll("[data-organisation-picker-button]").forEach((button) => {
    button.setAttribute("aria-expanded", "false");
    button.closest("[data-organisation-picker]")?.querySelector(".organisation-menu")?.setAttribute("hidden", "");
  });
});

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
  root.querySelectorAll("[data-local-time]").forEach((element) => {
    const value = element.getAttribute("datetime") || element.dataset.localTime;
    const formatted = formatLocalTimestamp(value);
    if (formatted) {
      element.textContent = formatted;
      element.setAttribute("title", value);
    }
  });

  root.querySelectorAll("[data-local-time-title]").forEach((element) => {
    const formatted = formatLocalTimestamp(element.dataset.localTimeTitle);
    if (formatted) {
      element.setAttribute("title", `${element.dataset.localTimeTitlePrefix || ""}${formatted}`);
    }
  });
}

document.addEventListener("DOMContentLoaded", () => {
  formatLocalTimes();

  const deviceEvents = document.querySelector("[data-device-events-url]");
  const releaseEvents = document.querySelector("[data-release-events-url]");
  if (!window.EventSource) {
    return;
  }

  let releaseRefreshInFlight = false;
  let releaseRefreshQueued = false;
  const refreshReleaseCVEState = async () => {
    const releaseState = document.querySelector("[data-release-cves-url]");
    if (!releaseState) {
      return;
    }
    if (releaseRefreshInFlight) {
      releaseRefreshQueued = true;
      return;
    }

    releaseRefreshInFlight = true;
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
      releaseRefreshInFlight = false;
      if (releaseRefreshQueued) {
        releaseRefreshQueued = false;
        refreshReleaseCVEState();
      }
    }
  };

  let deviceEventStream = null;
  if (deviceEvents) {
    deviceEventStream = new EventSource(deviceEvents.dataset.deviceEventsUrl);
    deviceEventStream.addEventListener("device-telemetry", () => {
      document.body.dispatchEvent(new Event("device-telemetry-refresh", { bubbles: true }));
    });
    deviceEventStream.addEventListener("device-tasks", () => {
      document.body.dispatchEvent(new Event("device-tasks-refresh", { bubbles: true }));
    });
  }

  let releaseEventStream = null;
  if (releaseEvents) {
    releaseEventStream = new EventSource(releaseEvents.dataset.releaseEventsUrl);
    releaseEventStream.addEventListener("open", () => {
      refreshReleaseCVEState();
    });
    releaseEventStream.addEventListener("release-cves", () => {
      refreshReleaseCVEState();
    });
  }

  window.addEventListener("beforeunload", () => {
    deviceEventStream?.close();
    releaseEventStream?.close();
  });
});

document.body.addEventListener("htmx:afterSwap", (event) => {
  formatLocalTimes(event.target);
});
