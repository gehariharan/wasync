// Copies the app's design system into the site, so the pages and the app share
// one set of files: internal/api/webassets is the only source. The copy lands in
// public/wasync/ui/ (git-ignored) and is refreshed before every dev run and deploy.
import { copyFileSync, mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const site = join(dirname(fileURLToPath(import.meta.url)), "..");
const source = join(site, "..", "internal", "api", "webassets");
const target = join(site, "public", "wasync", "ui");
const files = ["tokens.css", "components.css", "inter.woff2", "inter-license.txt", "icon.svg"];

mkdirSync(target, { recursive: true });
for (const file of files) copyFileSync(join(source, file), join(target, file));
console.log(`design system: copied ${files.length} files to public/wasync/ui`);
