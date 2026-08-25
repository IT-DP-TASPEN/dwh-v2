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

Alpine.data("confirmationDialog", () => ({
  form: null,
  trigger: null,
  title: "Confirm action",
  message: "Are you sure?",
  confirmLabel: "Confirm",
  open(event) {
    this.form = event.detail.form;
    this.trigger = event.detail.trigger;
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

document.addEventListener("submit", (event) => {
  const form = event.target;
  if (!(form instanceof HTMLFormElement) || !form.matches("[data-confirm]") || form.dataset.confirmed === "true") return;
  event.preventDefault();
  window.dispatchEvent(new CustomEvent("confirm-submit", { detail: { form, trigger: event.submitter } }));
});

const initializeIcons = () =>
  createIcons({
    icons: { Activity, CalendarClock, ChevronDown, CircleCheckBig, Database, FileCode, FileDown, History, LayoutDashboard, LogOut, Menu, PanelLeftClose, ScrollText, Server, Settings, Shield, ShieldCheck, Table2, UserRound, Users, X },
  });

document.addEventListener("DOMContentLoaded", initializeIcons);
document.body.addEventListener("htmx:afterSwap", initializeIcons);

Alpine.start();
