// Runs the Mac install-step renderer and checks the lists a visitor reads.
import assert from "node:assert/strict";
import { test } from "node:test";
import { fillMacSteps } from "../public/wasync/mac-steps.mjs";

const diskSteps = [
  "Drag wasync to Applications.",
  'Eject the "wasync" installer disk.',
  "Open wasync from Applications or Spotlight.",
];

function element(tag, attributes = {}) {
  return {
    tag,
    attributes: { ...attributes },
    children: [],
    text: "",
    set textContent(value) {
      this.text = value;
      this.children = [];
    },
    get textContent() {
      return this.children.length ? this.children.map(child => child.textContent).join("") : this.text;
    },
    getAttribute(name) { return this.attributes[name] ?? null; },
    replaceChildren(...nodes) { this.children = nodes; },
  };
}

function documentWith(...lists) {
  return {
    createElement: tag => element(tag),
    querySelectorAll(selector) {
      assert.equal(selector, "[data-mac-steps]");
      return lists;
    },
  };
}

test("the Mac download and install pages list the same three disk-image steps", () => {
  const download = element("ol", { "data-mac-steps": "download" });
  const install = element("ol", { "data-mac-steps": "install" });
  download.children = [element("p")];
  fillMacSteps(documentWith(download, install));

  const downloadText = download.children.map(item => item.textContent);
  const installText = install.children.map(item => item.textContent);
  assert.deepEqual(download.children.map(item => item.tag), ["li", "li", "li"]);
  assert.deepEqual(downloadText, diskSteps);
  assert.deepEqual(installText.slice(1, 4), diskSteps);
  assert.equal(installText[0], "Open the downloaded disk image.");
  assert.equal(installText.at(-1), "Scan the code with WhatsApp on your phone: Settings, Linked devices.");
  for (const step of downloadText) assert.equal(step.includes("\n"), false);
});

test("an unknown Mac install list is refused", () => {
  const list = element("ol", { "data-mac-steps": "pkg" });
  assert.throws(() => fillMacSteps(documentWith(list)), /unknown Mac install list pkg/);
});
