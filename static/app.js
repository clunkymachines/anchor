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

// Device selection

// Keep the checkbox inputs as the source of truth so the filter also works as
// a regular GET form, including with multiple tags and browser back/forward.
function updateTagFilters(form) {
  const selected = all('input[name="tag"]:checked', form);
  const count = form.querySelector("[data-tag-filter-count]");
  count.textContent = String(selected.length);
  count.hidden = selected.length === 0;
  const chips = form.querySelector("[data-active-filter-tags]");
  chips.replaceChildren();
  chips.hidden = selected.length === 0;
  selected.forEach((input) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "tag-chip filter-tag-chip";
    button.dataset.removeFilterTag = input.value;
    button.setAttribute("aria-label", `Remove ${input.value} filter`);
    const label = document.createElement("span");
    label.textContent = input.value;
    const remove = document.createElement("span");
    remove.textContent = "×";
    remove.setAttribute("aria-hidden", "true");
    button.append(label, remove);
    chips.append(button);
  });
}

function closeTagFilter(picker, restoreFocus = false) {
  picker.open = false;
  if (restoreFocus) picker.querySelector("summary").focus();
}

function handleDeviceFilterClick(event) {
  all("[data-tag-filter][open]").forEach((picker) => {
    if (!picker.contains(event.target)) closeTagFilter(picker);
  });
  const done = closestEventTarget(event, "[data-tag-filter-done]");
  if (done) closeTagFilter(done.closest("[data-tag-filter]"), true);

  const remove = closestEventTarget(event, "[data-remove-filter-tag]");
  if (remove) {
    const form = remove.closest("[data-device-filters]");
    all('input[name="tag"]', form).forEach((input) => {
      if (input.value === remove.dataset.removeFilterTag) input.checked = false;
    });
    updateTagFilters(form);
    form.requestSubmit();
  }

  if (closestEventTarget(event, "[data-clear-device-selection]")) {
    deviceSelectionInputs().forEach((input) => { input.checked = false; });
    updateDeviceSelectionState();
    document.querySelector("[data-select-all-visible]")?.focus();
  }
}

function searchTagFilters(input) {
  const picker = input.closest("[data-tag-filter]");
  const query = input.value.trim().toLowerCase();
  const options = all(".tag-filter-option", picker);
  options.forEach((option) => {
    option.hidden = !option.textContent.toLowerCase().includes(query);
  });
  const empty = picker.querySelector("[data-tag-filter-empty]");
  empty.hidden = options.some((option) => !option.hidden);
  empty.textContent = query ? "No matching tags." : "No tags available.";
}

function deviceSelectionInputs(root = document) {
  const form = root.querySelector("#campaign-selection-form");
  if (!form) {
    return [];
  }
  return all('input[name="device_id"][form="campaign-selection-form"]', root);
}

function updateDeviceSelectionState(root = document) {
  const checkboxes = deviceSelectionInputs(root);
  const selectedCount = checkboxes.filter((checkbox) => checkbox.checked).length;
  all("[data-campaign-submit]", root).forEach((submit) => { submit.disabled = selectedCount === 0; });
  all("[data-campaign-selection-hint]", root).forEach((hint) => { hint.hidden = selectedCount > 0; });
  all("[data-campaign-selection-label]", root).forEach((label) => {
    label.textContent = selectedCount ? `Create campaign (${selectedCount})` : "Create campaign";
  });
  all("[data-bulk-tag-form]", root).forEach((form) => { form.hidden = selectedCount === 0; });
  all("[data-device-selection-count]", root).forEach((count) => {
    count.textContent = `${selectedCount} selected`;
  });

  const selectAll = root.querySelector("[data-select-all-visible]");
  if (selectAll) {
    selectAll.checked = checkboxes.length > 0 && selectedCount === checkboxes.length;
    selectAll.indeterminate = selectedCount > 0 && selectedCount < checkboxes.length;
  }
}

// Campaign targeting

let campaignEstimateTimer;
let campaignEstimateRequest;

function updateCampaignTargeting(root = document) {
  const targeting = root.querySelector("[data-campaign-targeting]");
  const form = root.querySelector("#campaign-create-form");
  if (!targeting || !form) return;
  clearTimeout(campaignEstimateTimer);
  campaignEstimateRequest?.abort();
  const params = new URLSearchParams(new FormData(form));
  const submit = form.querySelector("[data-campaign-start]");
  const count = targeting.querySelector("[data-campaign-estimate]");
  const error = targeting.querySelector("[data-campaign-estimate-error]");
  if (params.get("target_type") === "explicit") {
    const selectedCount = params.getAll("device_id").length;
    count.textContent = String(selectedCount);
    submit.disabled = submit.dataset.unavailable === "true" || selectedCount === 0;
    return;
  }
  submit.disabled = true;
  if (!params.get("target_tag")?.trim() && !params.get("target_model_id")) {
    count.textContent = "—";
    error.textContent = "Choose a tag, a model, or both.";
    return;
  }
  count.textContent = "…";
  error.textContent = "";
  campaignEstimateRequest = new AbortController();
  const signal = campaignEstimateRequest.signal;
  campaignEstimateTimer = setTimeout(() => refreshCampaignEstimate(targeting, form, signal), 180);
}

async function refreshCampaignEstimate(targeting, form, signal) {
  const count = targeting.querySelector("[data-campaign-estimate]");
  const error = targeting.querySelector("[data-campaign-estimate-error]");
  const submit = form.querySelector("[data-campaign-start]");
  const params = new URLSearchParams(new FormData(form));
  for (const key of Array.from(params.keys())) {
    if (!["organisation_id", "target_type", "target_tag", "target_model_id", "device_id"].includes(key)) params.delete(key);
  }
  try {
    const response = await fetch(`${targeting.dataset.estimateUrl}?${params}`, { credentials: "same-origin", signal });
    const payload = await response.json();
    if (signal.aborted) return;
    if (!response.ok) throw new Error(payload.error?.message || "Choose a complete target.");
    count.textContent = String(payload.count);
    error.textContent = payload.count === 0 ? "No devices match these filters." : "";
    submit.disabled = submit.dataset.unavailable === "true" || payload.count === 0;
  } catch (requestError) {
    if (signal.aborted) return;
    count.textContent = "—";
    error.textContent = requestError.message;
    submit.disabled = true;
  }
}

function handleDeviceSelectionChange(event) {
  const selectAll = closestEventTarget(event, "[data-select-all-visible]");
  if (selectAll) {
    deviceSelectionInputs().forEach((checkbox) => {
      checkbox.checked = selectAll.checked;
    });
    updateDeviceSelectionState();
    return true;
  }

  if (closestEventTarget(event, 'input[name="device_id"][form="campaign-selection-form"]')) {
    updateDeviceSelectionState();
    return true;
  }
  return false;
}

function prepareBulkTagSubmission(event) {
  const form = event.target.closest?.("[data-bulk-tag-form]");
  if (!form) return;
  all("[data-bulk-device]", form).forEach((input) => input.remove());
  all('input[name="device_id"][form="campaign-selection-form"]:checked').forEach((checkbox) => {
    const hidden = document.createElement("input"); hidden.type = "hidden"; hidden.name = "device_id"; hidden.value = checkbox.value; hidden.dataset.bulkDevice = ""; form.append(hidden);
  });
  if (!form.querySelector('[data-bulk-device]')) { event.preventDefault(); window.alert("Select at least one device on this page."); }
}

// Device protocol configuration

function updateDeviceProtocolConfiguration(root = document) {
  const modelSelect = root.querySelector("[data-device-model-select]");
  if (!modelSelect) {
    return;
  }

  const protocol = (modelSelect.selectedOptions[0]?.dataset.protocol || "").toLowerCase();
  const panels = all("[data-device-protocol-config]", root);
  const supported = panels.some((panel) => panel.dataset.deviceProtocolConfig === protocol);

  panels.forEach((panel) => {
    const active = panel.dataset.deviceProtocolConfig === protocol;
    panel.hidden = !active;
    panel.classList.toggle("is-hidden", !active);
    all("input, select, textarea", panel).forEach((input) => {
      input.disabled = !active;
    });
  });

  const prompt = root.querySelector("[data-device-protocol-prompt]");
  if (prompt) {
    prompt.hidden = protocol !== "";
  }
  const unsupported = root.querySelector("[data-device-protocol-unsupported]");
  if (unsupported) {
    unsupported.hidden = protocol === "" || supported;
  }
  const submit = root.querySelector("[data-device-create-submit]");
  if (submit) {
    submit.disabled = !supported;
  }
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
  handleDeviceFilterClick(event);
  if (closestEventTarget(event, "[data-device-list-refresh]")) {
    window.location.reload();
    return;
  }
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
  const deviceSearch = closestEventTarget(event, "#device-filter-search");
  if (deviceSearch && event.key === "Enter" && !event.isComposing) {
    event.preventDefault();
    deviceSearch.form?.requestSubmit();
  }
  if (event.key === "Escape") {
    closeOrganisationPickers();
    all("[data-tag-filter][open]").forEach((picker) => closeTagFilter(picker, true));
  }
});

document.addEventListener("change", (event) => {
  handleDeviceSelectionChange(event);
  const filter = closestEventTarget(event, '[data-device-filters] input[name="tag"]');
  if (filter) updateTagFilters(filter.closest("[data-device-filters]"));
  if (closestEventTarget(event, "[data-device-model-select]")) {
    updateDeviceProtocolConfiguration();
  }
  if (closestEventTarget(event, '[name="target_type"], [name="target_tag"], [name="target_model_id"]')) updateCampaignTargeting();
});

document.addEventListener("input", (event) => {
  const search = closestEventTarget(event, "[data-tag-filter-search]");
  if (search) searchTagFilters(search);
  if (closestEventTarget(event, '[name="target_tag"]')) updateCampaignTargeting();
});

document.addEventListener("submit", prepareBulkTagSubmission);

document.addEventListener("DOMContentLoaded", () => {
  formatLocalTimes();
  updateDeviceSelectionState();
  all("[data-device-filters]").forEach(updateTagFilters);
  updateDeviceProtocolConfiguration();
  updateCampaignTargeting();

  const eventStreams = startEventStreams();
  window.addEventListener("beforeunload", () => {
    eventStreams.forEach((stream) => stream.close());
  });
});

window.addEventListener("pageshow", () => {
  all("[data-device-filters]").forEach(updateTagFilters);
  updateDeviceSelectionState();
});

document.body.addEventListener("htmx:beforeSwap", rememberTelemetryTabBeforeSwap);

document.body.addEventListener("htmx:afterSwap", (event) => {
  formatLocalTimes(event.target);
});

document.body.addEventListener("htmx:afterSettle", restoreRememberedTabsAfterSettle);
