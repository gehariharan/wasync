// Checks that every screenshot the landing page serves resolves to a real, web-sized file.
import assert from "node:assert/strict";
import { existsSync, readFileSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

const publicDir = join(dirname(fileURLToPath(import.meta.url)), "..", "public", "wasync");
const maxAssetBytes = 150_000;

function startTags(html) {
  const tags = [];
  const tagPattern = /<(\/?)([a-zA-Z][\w-]*)((?:\s+[^\s=>\/]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+))?)*)\s*\/?>/g;
  const attrPattern = /([^\s=>\/]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?/g;
  for (const [, closing, name, rawAttrs] of html.replace(/<!--[\s\S]*?-->/g, "").matchAll(tagPattern)) {
    const attrs = {};
    for (const [, key, dq, sq, bare] of rawAttrs.matchAll(attrPattern)) {
      attrs[key.toLowerCase()] = dq ?? sq ?? bare ?? "";
    }
    tags.push({ name: name.toLowerCase(), closing: closing === "/", attrs });
  }
  return tags;
}

function pictures(html) {
  const found = [];
  let current = null;
  for (const tag of startTags(html)) {
    if (tag.name === "picture") {
      if (tag.closing) {
        found.push(current);
        current = null;
      } else {
        current = { sources: [], imgs: [] };
      }
    } else if (current && !tag.closing && tag.name === "source") {
      current.sources.push(tag.attrs);
    } else if (current && !tag.closing && tag.name === "img") {
      current.imgs.push(tag.attrs);
    }
  }
  return found;
}

function assetFile(url) {
  const file = join(publicDir, url);
  assert.ok(existsSync(file), `page references missing asset ${url}`);
  assert.ok(statSync(file).size < maxAssetBytes, `${url} should stay under ${maxAssetBytes} bytes`);
  return file;
}

function pngSize(file) {
  const bytes = readFileSync(file);
  assert.equal(bytes.toString("latin1", 1, 4), "PNG", `${file} is not a PNG`);
  return { width: bytes.readUInt32BE(16), height: bytes.readUInt32BE(20) };
}

const landing = pictures(readFileSync(join(publicDir, "index.html"), "utf8"));

test("the landing page shows app screenshots", () => {
  assert.ok(landing.length > 0, "expected at least one <picture> screenshot");
});

test("every screenshot source and fallback image exists on disk and stays web-sized", () => {
  for (const picture of landing) {
    assert.ok(picture.sources.length > 0, "a <picture> should offer at least one <source>");
    for (const source of picture.sources) {
      assert.ok(source.srcset, "a <source> needs a srcset");
      for (const candidate of source.srcset.split(",")) {
        assetFile(candidate.trim().split(/\s+/)[0]);
      }
    }
    assert.equal(picture.imgs.length, 1, "a <picture> needs exactly one fallback <img>");
    assert.ok(picture.imgs[0].src, "the fallback <img> needs a src");
    assetFile(picture.imgs[0].src);
  }
});

test("each screenshot reserves the fallback image's real pixel size so the layout does not jump", () => {
  for (const picture of landing) {
    const img = picture.imgs[0];
    const actual = pngSize(assetFile(img.src));
    assert.deepEqual(
      { width: Number(img.width), height: Number(img.height) },
      actual,
      `${img.src} declares a size that differs from the image file`,
    );
  }
});
