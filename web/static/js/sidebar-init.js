(() => {
        const root = document.documentElement;
        try {
            const disclosures = window.__sidebarDisclosures || {};
            document.querySelectorAll("#admin-sidebar [data-navigation-disclosure]").forEach((item) => {
                const active = item.dataset.navigationActive === "true";
                const manualOpen = disclosures[item.dataset.navigationKey] === true;
                const open = active || manualOpen;
                item.dataset.navigationManualOpen = String(manualOpen);
                const button = item.querySelector("[data-navigation-button]");
                const panel = item.querySelector("[data-navigation-panel]");
                if (button) button.setAttribute("aria-expanded", String(open));
                if (panel) panel.style.display = open ? "" : "none";
            });
        } finally {
            root.removeAttribute("data-sidebar-disclosure-pending");
        }
    })();
document.addEventListener("htmx:afterSwap", () => {
 document.documentElement.removeAttribute("data-sidebar-disclosure-pending");
});

