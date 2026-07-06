# Clunky Machines — Web Application Design System

Version 0.2

## 1. Purpose

Anchor is a web-based device management application by Clunky Machines. The UI should feel technical, reliable, readable, and distinctive. It should inherit the Clunky Machines identity without becoming decorative or playful.

The interface must support both light and dark themes from the beginning. All colors should be implemented through semantic design tokens, not hard-coded component colors.

The visual foundation comes from the Clunky Machines brand system: Blue Metal, Amaranth, Vanilla, Pure White, Ice, and Zomp, with Gulax for expressive headings and Poppins for interface text.

## 2. Brand Tokens

Use the following raw brand tokens.

```css
:root {
  --cm-blue-metal: #343844;
  --cm-amaranth: #AA1D3D;
  --cm-vanilla: #FFF0B3;
  --cm-white: #FFFFFF;
  --cm-zomp: #429A86;
  --cm-ice: #CDE9F3;
}
```

Color roles:

```css
:root {
  --brand-primary: var(--cm-amaranth);
  --brand-structure: var(--cm-blue-metal);
  --brand-success: var(--cm-zomp);
  --brand-info: var(--cm-ice);
  --brand-warm: var(--cm-vanilla);
}
```

Do not use raw brand colors directly in components except in the root token file. Components should use semantic tokens such as `--surface-panel`, `--text-primary`, `--action-primary`, or `--status-success`.

## 3. Theme Tokens

### Light Theme

```css
:root,
:root[data-theme="light"] {
  color-scheme: light;

  --surface-app: #F8F8F6;
  --surface-sidebar: #FFFFFF;
  --surface-topbar: #FFFFFF;
  --surface-panel: #FFFFFF;
  --surface-panel-muted: #F3F5F6;
  --surface-raised: #FFFFFF;

  --text-primary: #343844;
  --text-secondary: rgba(52, 56, 68, 0.70);
  --text-muted: rgba(52, 56, 68, 0.52);
  --text-inverse: #FFFFFF;

  --border-subtle: rgba(52, 56, 68, 0.14);
  --border-strong: rgba(52, 56, 68, 0.24);

  --action-primary: #AA1D3D;
  --action-primary-hover: #941934;
  --action-primary-active: #7F142B;

  --status-success: #429A86;
  --status-success-bg: rgba(66, 154, 134, 0.14);

  --status-info: #4B9DB3;
  --status-info-bg: rgba(205, 233, 243, 0.65);

  --status-warning: #B88700;
  --status-warning-bg: rgba(255, 240, 179, 0.80);

  --status-danger: #AA1D3D;
  --status-danger-bg: rgba(170, 29, 61, 0.10);

  --shadow-card: 0 12px 32px rgba(52, 56, 68, 0.08);
  --shadow-floating: 0 20px 60px rgba(52, 56, 68, 0.16);
}
```

### Dark Theme

```css
:root[data-theme="dark"] {
  color-scheme: dark;

  --surface-app: #171B24;
  --surface-sidebar: #111620;
  --surface-topbar: #151A23;
  --surface-panel: #202632;
  --surface-panel-muted: #2A303D;
  --surface-raised: #262D3A;

  --text-primary: #FFFFFF;
  --text-secondary: rgba(255, 255, 255, 0.72);
  --text-muted: rgba(255, 255, 255, 0.50);
  --text-inverse: #343844;

  --border-subtle: rgba(255, 255, 255, 0.12);
  --border-strong: rgba(255, 255, 255, 0.24);

  --action-primary: #AA1D3D;
  --action-primary-hover: #C62B4F;
  --action-primary-active: #8D1732;

  --status-success: #5EC2AA;
  --status-success-bg: rgba(66, 154, 134, 0.18);

  --status-info: #CDE9F3;
  --status-info-bg: rgba(205, 233, 243, 0.14);

  --status-warning: #FFF0B3;
  --status-warning-bg: rgba(255, 240, 179, 0.16);

  --status-danger: #FF6F8B;
  --status-danger-bg: rgba(170, 29, 61, 0.22);

  --shadow-card: 0 12px 32px rgba(0, 0, 0, 0.28);
  --shadow-floating: 0 24px 80px rgba(0, 0, 0, 0.42);
}
```

## 4. Typography

Use two font roles.

```css
:root {
  --font-display: "Gulax", "Space Grotesk", system-ui, sans-serif;
  --font-ui: "Poppins", Inter, system-ui, sans-serif;
  --font-mono: "JetBrains Mono", "SFMono-Regular", Consolas, monospace;
}
```

Usage:

```css
.app-title,
.page-title,
.empty-state-title {
  font-family: var(--font-display);
}

body,
button,
input,
select,
textarea,
table {
  font-family: var(--font-ui);
}
```

Recommended scale:

```css
:root {
  --text-xs: 0.75rem;
  --text-sm: 0.875rem;
  --text-md: 1rem;
  --text-lg: 1.125rem;
  --text-xl: 1.375rem;
  --text-2xl: 2rem;
  --text-3xl: 3rem;
}
```

Use display typography for product identity and high-level page titles. Do not use it for tables, dense data, device identifiers, firmware versions, logs, or technical values.

## 5. Layout Shell

The application shell has four primary regions.

```html
<div class="app-shell">
  <aside class="app-sidebar">
    ...
  </aside>

  <div class="app-body">
    <header class="app-topbar">
      ...
    </header>

    <main class="app-workspace">
      ...
    </main>
  </div>

  <aside class="detail-drawer">
    ...
  </aside>
</div>
```

Base layout:

```css
.app-shell {
  min-height: 100vh;
  display: grid;
  grid-template-columns: var(--sidebar-width) minmax(0, 1fr);
  background: var(--surface-app);
  color: var(--text-primary);
}

.app-shell.has-detail-drawer {
  grid-template-columns: var(--sidebar-width) minmax(0, 1fr) var(--drawer-width);
}

:root {
  --sidebar-width: 248px;
  --drawer-width: 400px;
  --topbar-height: 76px;
  --workspace-padding: 24px;
}
```

Responsive behavior:

```css
@media (max-width: 1100px) {
  :root {
    --sidebar-width: 84px;
    --drawer-width: 360px;
  }

  .nav-label,
  .sidebar-brand-text,
  .organisation-picker-text {
    display: none;
  }
}

@media (max-width: 820px) {
  .app-shell,
  .app-shell.has-detail-drawer {
    grid-template-columns: 1fr;
  }

  .app-sidebar {
    position: fixed;
    inset: 0 auto 0 0;
    width: 280px;
    transform: translateX(-100%);
    z-index: 50;
  }

  .app-sidebar.is-open {
    transform: translateX(0);
  }

  .detail-drawer {
    position: fixed;
    inset: 0 0 0 auto;
    width: min(420px, 100vw);
    z-index: 60;
  }
}
```

## 6. Sidebar

The sidebar contains the brand block, organisation picker, navigation, utilities, and optional helper area.

```html
<aside class="app-sidebar">
  <div class="sidebar-brand">
    <div class="brand-mark">
      <img src="/logo.png" alt="Clunky Machines">
    </div>
    <div class="sidebar-brand-text">
      <strong>Anchor</strong>
      <span>by Clunky Machines</span>
    </div>
  </div>

  <button class="organisation-picker" type="button">
    <span class="organisation-avatar">CM</span>
    <span class="organisation-picker-text">
      <strong>Clunky Machines Lab</strong>
      <span>Production workspace</span>
    </span>
    <span class="organisation-chevron">⌄</span>
  </button>

  <nav class="sidebar-nav">
    ...
  </nav>
</aside>
```

CSS:

```css
.app-sidebar {
  background: var(--surface-sidebar);
  border-right: 1px solid var(--border-subtle);
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.sidebar-brand {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 22px 18px 14px;
}

.sidebar-brand strong {
  display: block;
  font-family: var(--font-display);
  font-size: 1.45rem;
  line-height: 1;
  color: var(--brand-primary);
}

.sidebar-brand span {
  display: block;
  margin-top: 4px;
  font-size: var(--text-xs);
  color: var(--text-secondary);
}
```

Use `logo.png` as the icon-only brand mark. It does not contain the product name, so it must be paired with visible `Anchor` text in standard app chrome.

## 7. Organisation Picker

The organisation picker is a persistent context control. It sits below the product brand and above navigation.

```css
.organisation-picker {
  display: grid;
  grid-template-columns: 36px minmax(0, 1fr) auto;
  align-items: center;
  gap: 10px;
  width: calc(100% - 32px);
  margin: 0 16px 18px;
  padding: 10px;
  border-radius: 14px;
  border: 1px solid var(--border-subtle);
  background: var(--surface-panel);
  color: var(--text-primary);
  cursor: pointer;
  text-align: left;
  transition:
    border-color 160ms ease,
    background-color 160ms ease,
    box-shadow 160ms ease,
    transform 160ms ease;
}

.organisation-picker:hover {
  border-color: var(--border-strong);
  box-shadow: var(--shadow-card);
}

.organisation-picker[aria-expanded="true"] {
  border-color: var(--action-primary);
  box-shadow: 0 0 0 3px rgba(170, 29, 61, 0.14);
}

.organisation-avatar {
  display: grid;
  place-items: center;
  width: 36px;
  height: 36px;
  border-radius: 10px;
  background: var(--brand-structure);
  color: var(--text-inverse);
  font-weight: 700;
  font-size: var(--text-xs);
}

:root[data-theme="dark"] .organisation-avatar {
  background: var(--brand-primary);
  color: #FFFFFF;
}
```

Dropdown menu:

```css
.organisation-menu {
  min-width: 280px;
  padding: 8px;
  border-radius: 16px;
  border: 1px solid var(--border-subtle);
  background: var(--surface-raised);
  box-shadow: var(--shadow-floating);
}

.organisation-menu-item {
  display: grid;
  grid-template-columns: 32px minmax(0, 1fr);
  gap: 10px;
  align-items: center;
  padding: 10px;
  border-radius: 10px;
  color: var(--text-primary);
}
```

## 8. Navigation Items

Navigation is componentized. Labels and routes are application data.

```html
<a class="nav-item is-active" href="#">
  <span class="nav-icon"></span>
  <span class="nav-label">Overview</span>
  <span class="nav-badge">8</span>
</a>
```

```css
.sidebar-nav {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 0 12px;
}

.nav-item {
  display: grid;
  grid-template-columns: 22px minmax(0, 1fr) auto;
  align-items: center;
  gap: 12px;
  min-height: 44px;
  padding: 0 12px;
  border-radius: 12px;
  color: var(--text-secondary);
  text-decoration: none;
}
```

## 9. Top Bar

The top bar hosts global controls. It should remain compact and not duplicate organisation context.

```css
.app-topbar {
  height: var(--topbar-height);
  display: grid;
  grid-template-columns: minmax(280px, 460px) minmax(0, 1fr) auto;
  align-items: center;
  gap: 16px;
  padding: 0 24px;
  background: var(--surface-topbar);
  border-bottom: 1px solid var(--border-subtle);
}
```

## 10. Workspace

```css
.app-workspace {
  padding: var(--workspace-padding);
  min-width: 0;
}

.page-header {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 20px;
  margin-bottom: 24px;
}

.page-title {
  margin: 0;
  font-family: var(--font-display);
  font-size: clamp(2rem, 4vw, 3rem);
  line-height: 1;
  color: var(--text-primary);
}
```

## 11. Page Types

Every workspace route should have one primary job. Do not combine a creation form, a list, and a detail surface on the same page just because they share a domain object. Prefer predictable page types and explicit navigation between them.

Allowed primary page types:

- `List`: shows a searchable/scannable collection, summary metrics when useful, row actions, and a primary create button in the page header.
- `Create form`: captures a new object. It should be reached from the list page primary action and should include a back link to the list.
- `Detail`: shows one existing object, its related state, and object-scoped actions. It may include small contextual forms for actions that are inseparable from the object state.
- `Edit/settings form`: updates an existing object or configuration. It should be reached from a detail or settings page action unless the entire page is dedicated to settings.
- `Authentication/onboarding`: login, invitation signup, and similar flows can be standalone forms outside the app workspace pattern.

Primary page rules:

- A list page must not embed a creation form above or beside the list. Put a `Create ...` button in `.page-actions` and route to `/resource/new`.
- A create page must not include the full collection list. It may show concise help, validation errors, and dependency warnings.
- A detail page may contain object-scoped operational forms, such as launching a device task, rescanning a release, replacing a release SBOM, or dismissing a specific CVE. These are not collection creation forms.
- Destructive row actions are allowed in tables when the action is local to the row and does not require a long form.
- Settings pages should avoid mixing unrelated settings forms with member or inventory lists. Split them into detail/settings subpages or use clearly scoped actions from the list/detail page.
- Empty list pages should still preserve the list page structure and offer the create action in the header or empty state, not an always-visible creation form.

Canonical route shape:

```text
/devices                 list
/devices/new             create form
/devices/{deviceID}      detail

/releases                list
/releases/new            create form
/releases/{releaseID}    detail

/device-models           list
/device-models/new       create form
/device-models/{id}      detail or edit/settings, when needed
```

Examples:

- The Devices page is the target pattern: it lists devices, exposes a `Create device` button, and sends users to a dedicated create form and device detail page.
- The Releases page should follow the same shape: list releases first, use `Create release` to open a dedicated upload form, and keep release CVE/SBOM operations on release detail.
- Device Models should be split into a list page plus a dedicated create form.
- Organisation membership should be treated as a list/detail/settings area. Rename and invite forms should not sit above member tables on the same default list surface.

Review checklist for new pages:

- Can the page be named as exactly one primary type?
- If the page contains a collection list, is the creation flow behind a button/link?
- If the page contains a create form, is it free of unrelated collection tables?
- If the page contains a detail view, are forms scoped to the current object?
- Are the primary and secondary actions in `.page-actions` predictable for that page type?

## 12. Cards

```css
.card {
  border: 1px solid var(--border-subtle);
  border-radius: 18px;
  background: var(--surface-panel);
  box-shadow: var(--shadow-card);
}

.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 16px 18px 0;
}
```

## 13. KPI Cards

```css
.kpi-card {
  display: grid;
  grid-template-columns: 52px minmax(0, 1fr);
  gap: 14px;
  align-items: center;
  padding: 18px;
}

.kpi-icon {
  width: 52px;
  height: 52px;
  display: grid;
  place-items: center;
  border-radius: 999px;
  background: var(--status-info-bg);
  color: var(--brand-structure);
}
```

## 14. Buttons

```css
.button {
  --button-bg: transparent;
  --button-fg: var(--text-primary);
  --button-border: var(--border-subtle);

  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  min-height: 40px;
  padding: 0 14px;
  border-radius: 11px;
  border: 1px solid var(--button-border);
  background: var(--button-bg);
  color: var(--button-fg);
  font-size: var(--text-sm);
  font-weight: 600;
  cursor: pointer;
  text-decoration: none;
}
```

## 15. Status Pills

```css
.status-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-height: 24px;
  padding: 0 9px;
  border-radius: 999px;
  font-size: var(--text-xs);
  font-weight: 600;
  white-space: nowrap;
}
```

Use explicit labels. Do not communicate state through color alone.

## 16. Tables

```css
.data-table-card {
  overflow: hidden;
}

.data-table-wrapper {
  width: 100%;
  overflow-x: auto;
}

.data-table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--text-sm);
}
```

For dense technical values:

```css
.value-mono {
  font-family: var(--font-mono);
  font-size: 0.875em;
}
```

## 17. Detail Drawer

```css
.detail-drawer {
  background: var(--surface-panel);
  border-left: 1px solid var(--border-subtle);
  min-width: 0;
  overflow-y: auto;
}
```

## 18. Forms

```css
.field {
  display: grid;
  gap: 6px;
}

.card-body > form {
  display: grid;
  gap: 14px;
}

.card-body > .form-actions {
  margin-top: 14px;
}

.card-body > .field + .field {
  margin-top: 14px;
}

.field-label {
  font-size: var(--text-sm);
  font-weight: 600;
  color: var(--text-primary);
}

.input,
.select,
.textarea {
  width: 100%;
  border-radius: 11px;
  border: 1px solid var(--border-subtle);
  background: var(--surface-panel);
  color: var(--text-primary);
  font: inherit;
  outline: none;
}
```

Card forms must have visible vertical rhythm between controls and actions. A submit/action row must not touch the previous combobox, input, textarea, file input, or help text; use the shared `.form-actions` spacing and the `.card-body > form` grid gap instead of one-off margins in templates.

## 19. Charts

Charts should inherit theme colors through CSS variables. Use Zomp for healthy/success trends, Amaranth for failures or high-priority events, Ice for neutral information, and Vanilla for warnings.

## 20. Empty States

```css
.empty-state {
  display: grid;
  place-items: center;
  min-height: 320px;
  padding: 32px;
  text-align: center;
  border: 1px dashed var(--border-strong);
  border-radius: 20px;
  background: var(--surface-panel);
}
```

The Clunky mascot can be used in empty states, onboarding, or help contexts. Avoid mascot usage in critical error states.

## 21. Motion

Use short, functional transitions.

```css
:root {
  --ease-standard: cubic-bezier(0.2, 0, 0, 1);
  --duration-fast: 120ms;
  --duration-normal: 180ms;
  --duration-slow: 260ms;
}
```

Respect reduced motion:

```css
@media (prefers-reduced-motion: reduce) {
  *,
  *::before,
  *::after {
    animation-duration: 0.001ms !important;
    animation-iteration-count: 1 !important;
    scroll-behavior: auto !important;
    transition-duration: 0.001ms !important;
  }
}
```

## 22. Date And Time

All persisted dates and timestamps must be stored in UTC. Server-side domain objects, database rows, scan results, telemetry event times, and API payload timestamps should use UTC values as the canonical representation.

The web UI must display dates and times in the user's browser locale and timezone. Prefer rendering a machine-readable UTC timestamp in markup, then formatting it client-side with `Intl.DateTimeFormat` or an equivalent browser API. Do not hard-code server-local timezone assumptions into templates.

Rules:

- Store and compare timestamps in UTC.
- Treat UTC as the API and database boundary format.
- Display timestamps using the browser locale/timezone.
- Preserve precise machine-readable values in attributes such as `datetime` or `data-timestamp` when client-side formatting is used.
- Avoid ambiguous date-only text for operational events; include time when recency, ordering, scans, telemetry, or deployments are involved.

## 23. Accessibility

Minimum implementation requirements:

```css
:focus-visible {
  outline: 2px solid var(--action-primary);
  outline-offset: 2px;
}
```

Rules:

- All interactive elements must be keyboard accessible.
- Buttons must use `<button>` unless navigation is intended.
- Icon-only buttons need `aria-label`.
- Status colors must be paired with text.
- Tables need proper `<th>` elements.
- Drawers and modals need focus management.
- Theme choice should persist across sessions.
- Text contrast must meet WCAG AA in both themes.

## 24. Naming Conventions

Use generic, product-stable class names.

Recommended:

```css
.app-shell {}
.app-sidebar {}
.sidebar-brand {}
.organisation-picker {}
.sidebar-nav {}
.nav-item {}
.app-topbar {}
.app-workspace {}
.page-header {}
.card {}
.data-table {}
.detail-drawer {}
.status-pill {}
```

Avoid product-specific names in base UI primitives. Product-specific classes can be added later at feature level.

## 25. Component Checklist

Initial implementation should include:

- AppShell
- Sidebar
- SidebarBrand
- OrganisationPicker
- OrganisationMenu
- NavGroup
- NavItem
- TopBar
- SearchField
- DropdownButton
- PageHeader
- Card
- KpiCard
- DataTable
- StatusPill
- ProgressBar
- ChartCard
- DetailDrawer
- Button
- IconButton
- Field
- Modal
- Toast
- EmptyState
- ThemeProvider
