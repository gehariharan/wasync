// Works out which download fits the visitor. Pure functions, so the tests in
// website/test can run them without a browser.

// The visitor's system from the user-agent string: "mac", "windows", "linux",
// "mobile" (phones and tablets, which cannot run wasync) or "any".
export function osFromUA(ua, touchPoints = 0) {
  if (/iPhone|iPod/.test(ua) || (/Android/.test(ua) && /Mobile/.test(ua))) return "mobile";
  // iPadOS Safari presents itself as a Mac; a touch screen gives it away.
  if (/iPad|Android/.test(ua) || (/Macintosh/.test(ua) && touchPoints > 1)) return "mobile";
  if (/Windows/.test(ua)) return "windows";
  if (/Macintosh|Mac OS X/.test(ua)) return "mac";
  if (/CrOS|Linux|X11/.test(ua)) return "linux";
  return "any";
}

// The chip, when the browser says. Chromium browsers report it through client
// hints; Safari and Firefox say "Intel Mac OS X" on every Mac, so a Mac without
// hints stays "unknown" and the page offers both Mac downloads. Hints are only
// trusted when they describe the same system as the user-agent string.
export function archFrom(os, ua, hints) {
  if (hints && /Chrome\//.test(ua)) {
    const agrees = (os === "mac" && hints.platform === "macOS") ||
      (os === "windows" && hints.platform === "Windows") ||
      (os === "linux" && /Linux|Chrome OS/.test(hints.platform || ""));
    if (agrees) {
      if (hints.architecture === "arm") return "arm64";
      if (hints.architecture === "x86" && hints.bitness === "64") return "amd64";
    }
  }
  if (os === "linux") {
    if (/aarch64|arm64/i.test(ua)) return "arm64";
    if (/x86_64|amd64/i.test(ua)) return "amd64";
  }
  return "unknown";
}

// The chip to assume for one platform's tile. The visitor's chip says nothing
// about another system, so other tiles treat it as unknown.
export function chipFor(tile, visitor) {
  return tile === visitor.os ? visitor.arch : "unknown";
}

// "0.1.0-preview.20261126105548" reads as "0.1.0 preview".
export function shortVersion(version) {
  return version.replace(/-preview\.\d+$/, " preview");
}

export function megabytes(bytes) {
  return `${(bytes / 1e6).toFixed(1)} MB`;
}
