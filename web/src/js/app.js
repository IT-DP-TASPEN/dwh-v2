import Alpine from "alpinejs";
import htmx from "htmx.org";
import {
  Activity,
  CalendarClock,
  ChevronDown,
  CircleCheckBig,
  createIcons,
  Database,
  FileCode,
  FileDown,
  History,
  Key,
  LayoutDashboard,
  LogOut,
  Menu,
  PanelLeftClose,
  ScrollText,
  Server,
  Settings,
  ShieldCheck,
  Shield,
  Table2,
  Users,
  UserRound,
  X,
} from "lucide";

window.Alpine = Alpine;
window.htmx = htmx;

const root = document.documentElement;
const systemTheme = window.matchMedia("(prefers-color-scheme: dark)");
const sidebarDisclosures = window.__sidebarDisclosures || Object.create(null);

const applyTheme = (preference) => {
  const dark = preference === "dark" || (preference === "system" && systemTheme.matches);
  root.dataset.themePreference = preference;
  root.classList.toggle("dark", dark);
  root.style.colorScheme = dark ? "dark" : "light";
};

Alpine.store("theme", {
  preference: root.dataset.themePreference || "system",
  init() {
    systemTheme.addEventListener("change", () => {
      if (this.preference === "system") applyTheme("system");
    });
  },
  set(preference) {
    if (!["light", "dark", "system"].includes(preference)) preference = "system";
    this.preference = preference;
    try {
      localStorage.setItem("theme", preference);
    } catch {}
    applyTheme(preference);
  },
});

Alpine.data("adminShell", () => ({
  sidebarCollapsed: root.dataset.sidebarCollapsed === "true",
  mobileOpen: false,
  init() {
    root.dataset.mobileSidebarOpen = "false";
    this.$watch("mobileOpen", (open) => {
      root.dataset.mobileSidebarOpen = String(open);
      document.body.classList.toggle("overflow-hidden", open);
    });
  },
  toggleSidebar() {
    this.sidebarCollapsed = !this.sidebarCollapsed;
    root.dataset.sidebarCollapsed = String(this.sidebarCollapsed);
    try {
      localStorage.setItem("sidebar-collapsed", String(this.sidebarCollapsed));
    } catch {}
  },
  expandSidebar() {
    if (!this.sidebarCollapsed) return;
    this.sidebarCollapsed = false;
    root.dataset.sidebarCollapsed = "false";
    try {
      localStorage.setItem("sidebar-collapsed", "false");
    } catch {}
  },
  closeMobile() {
    this.mobileOpen = false;
  },
}));

Alpine.data("navigationDisclosure", () => ({
  key: "",
  active: false,
  manualOpen: false,
  init() {
    this.key = this.$el.dataset.navigationKey;
    this.active = this.$el.dataset.navigationActive === "true";
    this.manualOpen = this.$el.dataset.navigationManualOpen === "true";
  },
  get open() {
    return this.active || this.manualOpen;
  },
  openDisclosure() {
    if (this.active || this.manualOpen) return;
    this.manualOpen = true;
    this.persistDisclosure();
  },
  toggleDisclosure() {
    if (this.active) return;
    this.manualOpen = !this.manualOpen;
    this.persistDisclosure();
  },
  persistDisclosure() {
    sidebarDisclosures[this.key] = this.manualOpen;
    try {
      localStorage.setItem("sidebar-disclosures", JSON.stringify(sidebarDisclosures));
    } catch {}
  },
}));

Alpine.data("contextMenu", () => ({
  open: false,
  left: 0,
  top: 0,
  maxHeight: 0,
  toggle() {
    if (this.open) {
      this.close(true);
      return;
    }
    window.dispatchEvent(new CustomEvent("context-menu-open", { detail: this.$el }));
    this.open = true;
    this.$nextTick(() => {
      this.place();
      this.$refs.first?.focus();
    });
  },
  place() {
    const trigger = this.$refs.trigger.getBoundingClientRect();
    const menu = this.$refs.menu;
    const margin = 8;
    const gap = 6;
    const width = menu.offsetWidth;
    this.left = Math.max(margin, Math.min(trigger.right - width, window.innerWidth - width - margin));

    const below = window.innerHeight - trigger.bottom - gap - margin;
    const above = trigger.top - gap - margin;
    const openAbove = below < Math.min(menu.scrollHeight, 160) && above > below;
    this.maxHeight = Math.max(48, openAbove ? above : below);
    const height = Math.min(menu.scrollHeight, this.maxHeight);
    this.top = openAbove ? trigger.top - gap - height : trigger.bottom + gap;
  },
  close(restoreFocus = false) {
    if (!this.open) return;
    this.open = false;
    if (restoreFocus) this.$nextTick(() => this.$refs.trigger?.focus());
  },
  viewportChanged() {
    this.close(this.$refs.menu?.contains(document.activeElement));
  },
}));

Alpine.data("confirmationDialog", () => ({
  form: null,
  trigger: null,
  title: "Confirm action",
  message: "Are you sure?",
  confirmLabel: "Confirm",
  open(event) {
    this.form = event.detail.form;
    this.trigger = document.getElementById(this.form.dataset.confirmCancelFocus || "") || event.detail.trigger;
    this.title = this.form.dataset.confirmTitle || "Confirm action";
    this.message = this.form.dataset.confirmMessage || "Are you sure?";
    this.confirmLabel = this.form.dataset.confirmLabel || "Confirm";
    this.$refs.dialog.showModal();
    this.$nextTick(() => this.$refs.cancel.focus());
  },
  cancel() {
    this.$refs.dialog.close();
  },
  confirm() {
    const form = this.form;
    const successFocus = document.getElementById(form.dataset.confirmSuccessFocus || "");
    if (successFocus) this.trigger = successFocus;
    this.$refs.dialog.close();
    form.dataset.confirmed = "true";
    form.requestSubmit();
    queueMicrotask(() => delete form.dataset.confirmed);
  },
  restoreFocus() {
    this.trigger?.focus();
    this.form = null;
    this.trigger = null;
  },
}));

Alpine.data("scheduleBulkActions", () => ({
  visibleIDs: [],
  selected: [],
  frozen: [],
  action: "",
  init() {
    this.visibleIDs = [...this.$root.querySelectorAll("[data-schedule-checkbox]")].map((input) => input.value);
  },
  get count() {
    return this.selected.length;
  },
  get allSelected() {
    return this.visibleIDs.length > 0 && this.selected.length === this.visibleIDs.length;
  },
  get partiallySelected() {
    return this.selected.length > 0 && !this.allSelected;
  },
  toggleAll(checked) {
    this.selected = checked ? [...this.visibleIDs] : [];
  },
  confirmAction(action) {
    if (!this.count) return;
    this.action = action;
    this.frozen = [...this.selected];
    const count = this.frozen.length;
    const schedules = `${count} ${count === 1 ? "schedule" : "schedules"}`;
    const copy = {
      enable: [`Enable ${schedules}?`, "These schedules will become eligible for scheduler execution again.", `Enable ${schedules}`],
      disable: [`Disable ${schedules}?`, "These schedules will stop being eligible for scheduler execution until enabled again.", `Disable ${schedules}`],
      archive: [`Archive ${schedules}?`, "This action is permanent. Archived schedules cannot be enabled or restored.", `Archive ${schedules}`],
    }[action];
    if (!copy) return;
    [this.$refs.form.dataset.confirmTitle, this.$refs.form.dataset.confirmMessage, this.$refs.form.dataset.confirmLabel] = copy;
    this.$nextTick(() => this.$refs.form.requestSubmit());
  },
}));

Alpine.data("copyDiagnostic", () => ({
  copied: false,
  async copy() {
    const text = this.$el.querySelector("[data-diagnostic-copy]")?.innerText || "";
    try {
      await navigator.clipboard.writeText(text);
      this.copied = true;
      setTimeout(() => (this.copied = false), 1600);
    } catch {
      this.copied = false;
    }
  },
}));

Alpine.data("reportResult", (elementID) => ({
  result: { columns: [], rows: [] },
  page: 1,
  pageSize: 50,
  init() {
    const element = document.getElementById(elementID);
    if (element) this.result = JSON.parse(element.textContent || "{}");
    this.result.columns ||= [];
    this.result.rows ||= [];
  },
  get pages() {
    return Math.max(1, Math.ceil(this.result.rows.length / this.pageSize));
  },
  get pageRows() {
    const start = (this.page - 1) * this.pageSize;
    return this.result.rows.slice(start, start + this.pageSize);
  },
  get pageLabel() {
    if (!this.result.rows.length) return "0 rows";
    const first = (this.page - 1) * this.pageSize + 1;
    return `${first}–${Math.min(first + this.pageSize - 1, this.result.rows.length)} of ${this.result.rows.length}`;
  },
  previous() {
    if (this.page > 1) this.page--;
  },
  next() {
    if (this.page < this.pages) this.page++;
  },
}));

Alpine.data("reportParameters", (elementID, reportID, enabled) => ({
  definitions: [],
  states: {},
  controllers: {},
  async init() {
    const element = document.getElementById(elementID);
    this.definitions = element ? JSON.parse(element.textContent || "[]") : [];
    for (const definition of this.definitions.filter((item) => item.option_source === "dynamic")) {
      this.states[definition.key] = { status: "idle", dependencies: null, options: [], selected: [], waitingFor: "", warning: "", generation: 0, present: Boolean(definition.present) };
    }
    this.$root.addEventListener("change", (event) => {
      const name = event.target?.name || "";
      if (!name.startsWith("param_")) return;
      const key = name.slice(6);
      const definition = this.definitions.find((item) => item.key === key);
      if (!definition || definition.option_source === "dynamic") return;
      this.cascade(key);
    });
    if (!enabled) return;
    for (const definition of this.definitions) {
      if (definition.option_source === "dynamic") await this.load(definition.key, true);
    }
  },
  stateFor(key) {
    return this.states[key] || { status: "idle", dependencies: [], options: [], selected: [], waitingFor: "", warning: "", present: false };
  },
  setState(key, changes) {
    this.states[key] = { ...this.stateFor(key), ...changes };
  },
  values(value) {
    if (Array.isArray(value)) return value.map(String).filter((item) => item !== "");
    return value == null || value === "" ? [] : [String(value)];
  },
  async load(key, initial = false) {
    const definition = this.definitions.find((item) => item.key === key);
    if (!definition || definition.option_source !== "dynamic" || !enabled) return;
    this.controllers[key]?.abort();
    const controller = new AbortController();
    this.controllers[key] = controller;
    const generation = this.stateFor(key).generation + 1;
    this.setState(key, { status: "loading", options: [], selected: [], waitingFor: "", warning: "", generation });
    try {
      const body = new URLSearchParams(new FormData(this.$root));
      const response = await fetch(`/reports/${reportID}/parameters/${key}/options`, { method: "POST", body, signal: controller.signal, headers: { Accept: "application/json" } });
      const result = await response.json();
      if (!response.ok || result.state === "error") throw new Error("option load failed");
      if (this.stateFor(key).generation !== generation) return;
      if (result.state === "waiting") {
        this.setState(key, { status: "waiting", dependencies: result.dependencies || [], waitingFor: result.waiting_for || "required parameter", options: [], selected: [] });
        return;
      }
      const options = result.options || [];
      const allowed = new Set(options.map((option) => option.value));
      const current = initial ? this.values(definition.current) : [];
      const defaults = this.values(definition.default);
      let selected = current.length && current.every((value) => allowed.has(value)) ? current : defaults.every((value) => allowed.has(value)) ? defaults : [];
      if (definition.type === "single_option") selected = selected.slice(0, 1);
      const invalidPreferred = (current.length && selected !== current && !current.every((value) => allowed.has(value))) || (defaults.length && !defaults.every((value) => allowed.has(value)));
      this.setState(key, { status: "ready", dependencies: result.dependencies || [], options, selected, waitingFor: "", warning: result.warning || (invalidPreferred ? "Saved value is not available. Choose a current option." : "") });
      await this.$nextTick();
    } catch (error) {
      if (error.name === "AbortError" || this.stateFor(key).generation !== generation) return;
      this.setState(key, { status: "error", options: [], selected: [], waitingFor: "", warning: "" });
    }
  },
  dynamicChanged(key, event) {
    const definition = this.definitions.find((item) => item.key === key);
    if (!definition) return;
    const selected = definition.type === "multiple_option"
      ? [...this.$root.querySelectorAll(`[name="param_${key}"]:checked`)].map((input) => input.value)
      : event.target.value ? [event.target.value] : [];
    this.setState(key, { selected, present: true });
    this.cascade(key);
  },
  async cascade(changedKey) {
    const changed = new Set([changedKey]);
    const changedIndex = this.definitions.findIndex((item) => item.key === changedKey);
    const descendants = [];
    for (let pass = 0; pass < this.definitions.length; pass++) {
      for (let index = 0; index < this.definitions.length; index++) {
        const definition = this.definitions[index];
        if (definition.option_source !== "dynamic" || changed.has(definition.key)) continue;
        const dependencies = this.stateFor(definition.key).dependencies;
        if ((dependencies === null && index > changedIndex) || dependencies?.some((key) => changed.has(key))) {
          changed.add(definition.key);
          if (!descendants.includes(definition.key)) descendants.push(definition.key);
        }
      }
    }
    for (const key of descendants) {
      this.controllers[key]?.abort();
      this.setState(key, { status: "idle", options: [], selected: [], waitingFor: "", warning: "", generation: this.stateFor(key).generation + 1 });
    }
    for (const key of descendants) await this.load(key, false);
  },
  async retry(key) {
    await this.load(key, false);
    if (this.stateFor(key).status === "ready") await this.cascade(key);
  },
  get submittable() {
    return this.definitions.every((definition) => {
      if (definition.option_source !== "dynamic" || !definition.required) return true;
      const state = this.stateFor(definition.key);
      return state.status === "ready" && state.selected.length > 0;
    });
  },
}));

Alpine.data("reportTemplateEditor", () => ({
  parameters: [],
  nextID: 1,
  init() {
    const parsedDefinitions = this.parse(this.$refs.parametersJSON.value, []);
    const parsedTestValues = this.parse(this.$refs.testValuesJSON.value, {});
    const definitions = Array.isArray(parsedDefinitions) ? parsedDefinitions : [];
    const testValues = parsedTestValues && typeof parsedTestValues === "object" && !Array.isArray(parsedTestValues) ? parsedTestValues : {};
    this.parameters = definitions.map((definition) => this.parameter(definition, testValues));
  },
  parse(value, fallback) {
    try {
      return JSON.parse(value || "") ?? fallback;
    } catch {
      return fallback;
    }
  },
  parameter(definition = {}, testValues = {}) {
    const type = ["text", "integer", "decimal", "date", "datetime", "boolean", "single_option", "multiple_option"].includes(definition.type) ? definition.type : "text";
	const isOption = type === "single_option" || type === "multiple_option";
	const optionSource = isOption && definition.option_source === "dynamic" ? "dynamic" : isOption ? "static" : "";
    const hasTestValue = Object.prototype.hasOwnProperty.call(testValues, definition.key);
	const defaultValue = this.controlValue(type, definition.default);
	const testValue = this.controlValue(type, hasTestValue ? testValues[definition.key] : definition.default);
    return {
      id: this.nextID++,
      key: definition.key || "",
      label: definition.label || "",
      type,
      previousType: type,
	  optionSource,
	  previousOptionSource: optionSource,
	  dynamicOptionSQL: definition.dynamic_option_sql || "",
      required: Boolean(definition.required),
	  defaultValue,
	  dynamicDefaultText: Array.isArray(defaultValue) ? defaultValue.join("\n") : "",
	  testValue,
	  dynamicTestText: Array.isArray(testValue) ? testValue.join("\n") : "",
      testTouched: hasTestValue,
      options: (definition.options || []).map((option) => ({ id: this.nextID++, value: option.value || "", label: option.label || "" })),
    };
  },
  controlValue(type, value) {
    if (type === "multiple_option") return Array.isArray(value) ? value.map(String) : value == null ? [] : [String(value)];
    if (value == null || Array.isArray(value)) return "";
    return String(value);
  },
  addParameter() {
    this.parameters.push(this.parameter());
  },
  removeParameter(index) {
    this.parameters.splice(index, 1);
  },
  move(list, index, offset) {
    const target = index + offset;
    if (target < 0 || target >= list.length) return;
    list.splice(target, 0, list.splice(index, 1)[0]);
  },
  typeChanged(parameter) {
    const wasOption = parameter.previousType === "single_option" || parameter.previousType === "multiple_option";
    const isOption = parameter.type === "single_option" || parameter.type === "multiple_option";
	if (!wasOption || !isOption) {
	  parameter.options = [];
	  parameter.dynamicOptionSQL = "";
	  parameter.optionSource = isOption ? "static" : "";
	  parameter.previousOptionSource = parameter.optionSource;
	}
    parameter.defaultValue = parameter.type === "multiple_option" ? [] : "";
	parameter.dynamicDefaultText = "";
    parameter.testValue = parameter.type === "multiple_option" ? [] : "";
	parameter.dynamicTestText = "";
    parameter.testTouched = false;
    parameter.previousType = parameter.type;
  },
	sourceChanged(parameter) {
	  if (parameter.optionSource === parameter.previousOptionSource) return;
	  parameter.options = [];
	  parameter.dynamicOptionSQL = "";
	  parameter.defaultValue = parameter.type === "multiple_option" ? [] : "";
	  parameter.dynamicDefaultText = "";
	  parameter.testValue = parameter.type === "multiple_option" ? [] : "";
	  parameter.dynamicTestText = "";
	  parameter.testTouched = false;
	  parameter.previousOptionSource = parameter.optionSource;
	},
	availableUpstream(index) {
	  const result = [];
	  for (const parameter of this.parameters.slice(0, index)) {
		if (!parameter.key) continue;
		result.push(`:${parameter.key}`);
		if (parameter.type === "multiple_option") result.push(`:${parameter.key}__count`);
	  }
	  return result;
	},
	syncDynamicDefault(parameter) {
	  parameter.defaultValue = parameter.dynamicDefaultText.split("\n").filter((value) => value !== "");
	  this.syncDefault(parameter);
	},
	syncDynamicTest(parameter) {
	  parameter.testValue = parameter.dynamicTestText.split("\n").filter((value) => value !== "");
	  parameter.testTouched = true;
	},
  syncDefault(parameter) {
    if (!parameter.testTouched) parameter.testValue = Array.isArray(parameter.defaultValue) ? [...parameter.defaultValue] : parameter.defaultValue;
  },
  addOption(parameter) {
    parameter.options.push({ id: this.nextID++, value: "", label: "" });
  },
  removeOption(parameter, index) {
    const [removed] = parameter.options.splice(index, 1);
    if (!removed) return;
    if (Array.isArray(parameter.defaultValue)) parameter.defaultValue = parameter.defaultValue.filter((value) => value !== removed.value);
    if (Array.isArray(parameter.testValue)) parameter.testValue = parameter.testValue.filter((value) => value !== removed.value);
    if (parameter.defaultValue === removed.value) parameter.defaultValue = "";
    if (parameter.testValue === removed.value) parameter.testValue = "";
  },
  encodedValue(parameter, value) {
    if (parameter.type === "multiple_option") return value;
    if (value === "") return null;
    if (parameter.type === "boolean") return value === "true";
    return value;
  },
  serialize() {
    this.$refs.parametersJSON.value = JSON.stringify(this.parameters.map((parameter) => ({
      key: parameter.key,
      label: parameter.label,
      type: parameter.type,
      required: parameter.required,
      default: this.encodedValue(parameter, parameter.defaultValue),
	  ...(parameter.type === "single_option" || parameter.type === "multiple_option" ? {
		option_source: parameter.optionSource,
		...(parameter.optionSource === "static" ? { options: parameter.options.map(({ value, label }) => ({ value, label })) } : { dynamic_option_sql: parameter.dynamicOptionSQL }),
	  } : {}),
    })));
    const testValues = {};
    for (const parameter of this.parameters) {
      if (parameter.key && parameter.testTouched) testValues[parameter.key] = this.encodedValue(parameter, parameter.testValue);
    }
    this.$refs.testValuesJSON.value = JSON.stringify(testValues);
  },
}));

document.addEventListener("submit", (event) => {
  const form = event.target;
  if (!(form instanceof HTMLFormElement) || !form.matches("[data-confirm]") || form.dataset.confirmed === "true") return;
  event.preventDefault();
  window.dispatchEvent(new CustomEvent("confirm-submit", { detail: { form, trigger: event.submitter } }));
});

const initializeIcons = () =>
  createIcons({
    icons: { Activity, CalendarClock, ChevronDown, CircleCheckBig, Database, FileCode, FileDown, History, Key, LayoutDashboard, LogOut, Menu, PanelLeftClose, ScrollText, Server, Settings, Shield, ShieldCheck, Table2, UserRound, Users, X },
  });

document.addEventListener("DOMContentLoaded", initializeIcons);
document.body.addEventListener("htmx:afterSwap", initializeIcons);

Alpine.start();
