// Shared by /wasync/ (landing) and /wasync/installoptions/ (install options).
// Every link works without this script: /wasync/download/<key> is a redirect the
// Worker resolves to the newest release file.
import { archFrom, chipFor, megabytes, osFromUA, shortVersion } from "./detect.mjs";
import { fillMacSteps } from "./mac-steps.mjs";

const base = document.body.dataset.base || "";

async function detect() {
  const ua = navigator.userAgent;
  const os = osFromUA(ua, navigator.maxTouchPoints || 0);
  let hints = null;
  const data = navigator.userAgentData;
  if (data && data.getHighEntropyValues) {
    try {
      hints = { platform: data.platform, ...(await data.getHighEntropyValues(["architecture", "bitness"])) };
    } catch { /* the chip stays unknown */ }
  }
  return { os, arch: archFrom(os, ua, hints) };
}

function selectTab(os) {
  document.querySelectorAll("[data-tab]").forEach(tab => {
    const selected = tab.dataset.tab === os;
    tab.setAttribute("aria-selected", String(selected));
    tab.tabIndex = selected || (os === "any" && tab.dataset.tab === "mac") ? 0 : -1;
  });
  document.querySelectorAll("[data-panel]").forEach(panel => { panel.hidden = panel.dataset.panel !== os; });
}

function show(visitor) {
  const { os, arch } = visitor;
  document.body.dataset.os = os;
  document.body.dataset.arch = arch;
  document.querySelectorAll("[data-for]").forEach(el => { el.hidden = !el.dataset.for.split(" ").includes(os); });
  const macChip = chipFor("mac", visitor);
  document.querySelectorAll("[data-arch-for]").forEach(el => { el.hidden = !el.dataset.archFor.split(" ").includes(macChip); });
  if (os !== "mobile") selectTab(os);
  if (chipFor("linux", visitor) === "arm64") {
    const button = document.querySelector("[data-linux-btn]");
    if (button) { button.href = `${base}download/linux-arm64`; button.dataset.dl = "linux-arm64"; }
    const meta = document.querySelector("[data-linux-meta]");
    if (meta) meta.textContent = "ARM64";
  }
  // Highlight the visitor's own rows in the install options table.
  const rows = [...document.querySelectorAll("tr[data-row]")].filter(tr => tr.dataset.row === os);
  const exact = rows.filter(tr => tr.querySelector(`[data-dl$="-${arch}"]`));
  for (const tr of exact.length ? exact : rows) tr.classList.add("here");
}

function wireTabs() {
  const tabs = [...document.querySelectorAll("[data-tab]")];
  tabs.forEach((tab, i) => {
    tab.addEventListener("click", () => selectTab(tab.dataset.tab));
    tab.addEventListener("keydown", event => {
      const step = event.key === "ArrowRight" ? 1 : event.key === "ArrowLeft" ? -1 : 0;
      if (!step) return;
      const next = tabs[(i + step + tabs.length) % tabs.length];
      selectTab(next.dataset.tab);
      next.focus();
    });
  });
}

function fill(release) {
  const date = new Intl.DateTimeFormat(undefined, { dateStyle: "medium" }).format(new Date(release.published_at));
  const version = shortVersion(release.version);
  const short = document.querySelector('[data-fill="version-short"]');
  if (short) short.textContent = `Version ${version}, ${date}`;
  const line = document.querySelector('[data-fill="release-line"]');
  if (line) line.textContent = `Version ${version}, released ${date}. Every file comes from this release on GitHub.`;
  const sums = document.querySelector('[data-fill="checksums"]');
  if (sums) sums.href = release.checksums_url;
  const notes = document.querySelector('[data-fill="notes"]');
  if (notes) notes.href = release.html_url;
  for (const [key, item] of Object.entries(release.downloads)) {
    document.querySelectorAll(`[data-size="${key}"]`).forEach(el => { el.textContent = megabytes(item.size); });
    document.querySelectorAll(`[data-meta="${key}"]`).forEach(el => {
      el.dataset.kind ||= el.textContent;
      el.textContent = `${el.dataset.kind} · ${megabytes(item.size)}`;
    });
    document.querySelectorAll(`[data-dl="${key}"]`).forEach(a => { a.title = item.file; });
  }
  const { os, arch } = document.body.dataset;
  const linuxKey = chipFor("linux", { os, arch }) === "arm64" ? "linux-arm64" : "linux-amd64";
  const linux = release.downloads[linuxKey];
  if (linux) {
    document.querySelectorAll("[data-linux-file]").forEach(el => { el.textContent = linux.file; });
    const meta = document.querySelector("[data-linux-meta]");
    if (meta) meta.textContent = `${linuxKey === "linux-arm64" ? "ARM64" : "x86-64"} · .tar.gz · ${megabytes(linux.size)}`;
  }
}

function wireCopy() {
  const live = document.getElementById("live");
  document.querySelectorAll("[data-copy]").forEach(button => {
    button.addEventListener("click", async () => {
      const source = document.getElementById(button.dataset.copy);
      try {
        await navigator.clipboard.writeText(source.textContent);
        const was = button.textContent;
        button.textContent = "Copied";
        live.textContent = "Copied to the clipboard.";
        setTimeout(() => { button.textContent = was; }, 1800);
      } catch {
        live.textContent = "Copy did not work. Select the text and copy it with your keyboard.";
      }
    });
  });
  const share = document.querySelector("[data-share]");
  if (share) {
    if (!navigator.share) share.hidden = true;
    share.addEventListener("click", () => navigator.share({
      title: "wasync",
      text: "Open this on your computer to download wasync.",
      url: "https://gehariharan.com/wasync/",
    }).catch(() => {}));
  }
}

fillMacSteps(document);
wireCopy();
wireTabs();
detect().then(show).then(() => fetch(`${base}releases.json`))
  .then(response => {
    // 404: no release carries the app installers yet.
    if (response.status === 404) document.querySelectorAll('[data-state="no-release"]').forEach(n => { n.hidden = false; });
    return response.ok ? response.json() : Promise.reject(new Error(String(response.status)));
  })
  .then(fill)
  .catch(() => { /* every link still works through the Worker redirects */ });
