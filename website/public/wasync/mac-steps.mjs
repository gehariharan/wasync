// Mac disk-image install steps. app.js writes these into [data-mac-steps] lists
// so the download page and the install-options page stay the same three steps.

export const macInstallSteps = Object.freeze([
  "Drag wasync to Applications.",
  'Eject the "wasync" installer disk.',
  "Open wasync from Applications or Spotlight.",
]);

// The install-options page also says to open the disk image and link WhatsApp.
export const macInstallPageSteps = Object.freeze([
  "Open the downloaded disk image.",
  ...macInstallSteps,
  "Scan the code with WhatsApp on your phone: Settings, Linked devices.",
]);

const lists = {
  download: macInstallSteps,
  install: macInstallPageSteps,
};

export function fillMacSteps(doc) {
  for (const list of doc.querySelectorAll("[data-mac-steps]")) {
    const kind = list.getAttribute("data-mac-steps");
    const steps = lists[kind];
    if (!steps) throw new Error(`unknown Mac install list ${kind}`);
    list.replaceChildren(...steps.map(text => {
      const item = doc.createElement("li");
      item.textContent = text;
      return item;
    }));
  }
}
