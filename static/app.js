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

  const launchButton = event.target.closest("[data-task-launch-open]");
  if (launchButton) {
    const panel = document.querySelector("[data-task-launch-panel]");
    panel?.classList.remove("is-hidden");
    panel?.querySelector('[data-task-kind="fota"]')?.focus();
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

  const taskKind = event.target.closest("[data-task-kind]");
  if (taskKind) {
    const panel = taskKind.closest("[data-task-launch-panel]");
    if (!panel) {
      return;
    }
    const kind = taskKind.dataset.taskKind;
    panel.querySelectorAll("[data-task-kind]").forEach((button) => {
      button.classList.toggle("is-active", button === taskKind);
    });
    panel.querySelectorAll("[data-task-form]").forEach((form) => {
      form.classList.toggle("is-hidden", form.dataset.taskForm !== kind);
    });
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

document.addEventListener("DOMContentLoaded", () => {
  const deviceEvents = document.querySelector("[data-device-events-url]");
  if (!deviceEvents || !window.EventSource) {
    return;
  }

  const deviceEventStream = new EventSource(deviceEvents.dataset.deviceEventsUrl);
  deviceEventStream.addEventListener("device-telemetry", () => {
    document.body.dispatchEvent(new Event("device-telemetry-refresh", { bubbles: true }));
  });
  deviceEventStream.addEventListener("device-tasks", () => {
    document.body.dispatchEvent(new Event("device-tasks-refresh", { bubbles: true }));
  });
  window.addEventListener("beforeunload", () => {
    deviceEventStream.close();
  });
});
