// Which download the pages offer for real browsers' identities.
import assert from "node:assert/strict";
import { test } from "node:test";
import { archFrom, forcedPlatform, megabytes, osFromUA, shortVersion } from "../public/wasync/detect.mjs";

const ua = {
  macChrome: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/158.0.0.0 Safari/537.36",
  macSafari: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Safari/605.1.15",
  macFirefox: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:150.0) Gecko/20100101 Firefox/150.0",
  windowsEdge: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/158.0.0.0 Safari/537.36 Edg/158.0.0.0",
  linuxFirefox: "Mozilla/5.0 (X11; Linux x86_64; rv:150.0) Gecko/20100101 Firefox/150.0",
  linuxArm: "Mozilla/5.0 (X11; Linux aarch64; rv:150.0) Gecko/20100101 Firefox/150.0",
  chromebook: "Mozilla/5.0 (X11; CrOS x86_64 16000.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/158.0.0.0 Safari/537.36",
  iphone: "Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Mobile/15E148 Safari/604.1",
  androidPhone: "Mozilla/5.0 (Linux; Android 16; Pixel 10) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/158.0.0.0 Mobile Safari/537.36",
  androidTablet: "Mozilla/5.0 (Linux; Android 16; Pixel Tablet) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/158.0.0.0 Safari/537.36",
};

test("each system gets its own download", () => {
  assert.equal(osFromUA(ua.macChrome), "mac");
  assert.equal(osFromUA(ua.macSafari), "mac");
  assert.equal(osFromUA(ua.windowsEdge), "windows");
  assert.equal(osFromUA(ua.linuxFirefox), "linux");
  assert.equal(osFromUA(ua.chromebook), "linux");
  assert.equal(osFromUA("curl/8.0"), "any");
});

test("phones and tablets, including iPads that present themselves as Macs, get no download", () => {
  assert.equal(osFromUA(ua.iphone), "mobile");
  assert.equal(osFromUA(ua.androidPhone), "mobile");
  assert.equal(osFromUA(ua.androidTablet), "mobile");
  assert.equal(osFromUA(ua.macSafari, 5), "mobile");
  assert.equal(osFromUA(ua.macSafari, 0), "mac");
});

test("a Mac's chip is known only when a Chromium browser reports it", () => {
  assert.equal(archFrom("mac", ua.macChrome, { platform: "macOS", architecture: "arm", bitness: "64" }), "arm64");
  assert.equal(archFrom("mac", ua.macChrome, { platform: "macOS", architecture: "x86", bitness: "64" }), "amd64");
  // Safari and Firefox say "Intel" on every Mac and have no client hints.
  assert.equal(archFrom("mac", ua.macSafari, null), "unknown");
  assert.equal(archFrom("mac", ua.macFirefox, null), "unknown");
  // Hints that describe another system (an overridden user agent) are ignored.
  assert.equal(archFrom("mac", ua.macChrome, { platform: "Windows", architecture: "arm" }), "unknown");
});

test("Linux reads the chip from the user agent; Windows on Arm is reported", () => {
  assert.equal(archFrom("linux", ua.linuxFirefox, null), "amd64");
  assert.equal(archFrom("linux", ua.linuxArm, null), "arm64");
  assert.equal(archFrom("windows", ua.windowsEdge, { platform: "Windows", architecture: "arm", bitness: "64" }), "arm64");
  assert.equal(archFrom("windows", ua.windowsEdge, null), "unknown");
});

test("support links can force a view, and nothing else", () => {
  assert.deepEqual(forcedPlatform("?platform=windows"), { os: "windows", arch: "unknown" });
  assert.deepEqual(forcedPlatform("?platform=mac-amd64"), { os: "mac", arch: "amd64" });
  assert.equal(forcedPlatform("?platform=solaris"), null);
  assert.equal(forcedPlatform("?platform=mac-<script>"), null);
  assert.equal(forcedPlatform(""), null);
});

test("versions and sizes read naturally", () => {
  assert.equal(shortVersion("0.1.0-preview.20261126105548"), "0.1.0 preview");
  assert.equal(shortVersion("0.2.0"), "0.2.0");
  assert.equal(megabytes(12320130), "12.3 MB");
});
