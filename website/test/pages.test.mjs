// Checks the landing page markup, stylesheet rules, and screenshot assets.
import assert from "node:assert/strict";
import { existsSync, readFileSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

const websiteDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const publicDir = join(websiteDir, "public", "wasync");
const landingHTML = readFileSync(join(publicDir, "index.html"), "utf8");
const installHTML = readFileSync(join(publicDir, "installoptions", "index.html"), "utf8");
const siteCSS = readFileSync(join(publicDir, "site.css"), "utf8");

test("landing page keeps the platform picker, mobile view, and download links intact", () => {
  for (const tab of ["mac", "windows", "linux"]) {
    assert.match(landingHTML, new RegExp(`id="tab-${tab}"[^>]*aria-controls="panel-${tab}"`));
    assert.match(landingHTML, new RegExp(`id="panel-${tab}"[^>]*data-panel="${tab}"`));
  }
  for (const key of ["mac-arm64", "mac-amd64", "windows-amd64", "linux-amd64"]) {
    assert.match(landingHTML, new RegExp(`href="download/${key}"[^>]*data-dl="${key}"`));
  }
  assert.match(landingHTML, /data-for="mobile"/);
  assert.match(landingHTML, /data-share/);
  assert.match(landingHTML, /data-copy="page-link"/);
  assert.match(landingHTML, /href="installoptions\/#not-whatsapp"/);
  assert.match(installHTML, /id="not-whatsapp"/);
});

test("hero icon uses an inline animated SVG and site.css respects prefers-reduced-motion", () => {
  assert.match(landingHTML, /<svg class="hero-logo"[^>]*aria-hidden="true"/);
  assert.match(landingHTML, /class="hl-tile"/);
  assert.match(landingHTML, /class="hl-arrows"/);
  assert.match(siteCSS, /@keyframes hl-spin/);
  assert.match(siteCSS, /@media \(prefers-reduced-motion: reduce\)/);
});

test("header sits on the page background without the white bar from components.css", () => {
  assert.match(siteCSS, /body\.site \.topbar\s*\{[^}]*background:\s*transparent;/s);
  assert.match(siteCSS, /body\.site \.topbar\s*\{[^}]*border-bottom:\s*0;/s);
});

test("screenshots section has lazy-loaded WebP and fallback images with explicit dimensions on disk", () => {
  assert.match(landingHTML, /<section class="showcase" aria-labelledby="showcase-title">/);
  const pictures = [...landingHTML.matchAll(/<picture>([\s\S]*?)<\/picture>/g)];
  assert.equal(pictures.length, 3, "expected 3 app screenshots in the showcase");

  for (const [, inner] of pictures) {
    const sources = [...inner.matchAll(/<source\s+([^>]+)>/g)].map(m => m[1]);
    assert.ok(sources.some(s => /type="image\/webp"/.test(s) && /prefers-color-scheme: dark/.test(s)), "expected dark WebP source");
    assert.ok(sources.some(s => /type="image\/webp"/.test(s) && !/prefers-color-scheme/.test(s)), "expected light WebP source");

    for (const src of sources) {
      const file = /srcset="([^"]+)"/.exec(src)?.[1];
      assert.ok(file, "source must have srcset");
      const abs = join(publicDir, file);
      assert.ok(existsSync(abs), `missing asset ${file}`);
      assert.ok(statSync(abs).size < 150_000, `WebP asset ${file} should stay web-friendly (<150 KB)`);
    }

    const imgTag = /<img\s+([^>]+)>/.exec(inner)?.[1];
    assert.ok(imgTag, "picture must have fallback <img>");
    assert.match(imgTag, /loading="lazy"/);
    assert.match(imgTag, /decoding="async"/);
    assert.match(imgTag, /width="\d+"/);
    assert.match(imgTag, /height="\d+"/);
    assert.match(imgTag, /alt="[^"]{10,}"/);
    const imgSrc = /src="([^"]+)"/.exec(imgTag)?.[1];
    assert.ok(imgSrc, "img must have src");
    const absImg = join(publicDir, imgSrc);
    assert.ok(existsSync(absImg), `missing fallback image ${imgSrc}`);
    assert.ok(statSync(absImg).size < 150_000, `fallback image ${imgSrc} should stay web-friendly (<150 KB)`);
  }
});
