// Runs the Worker with a fake GitHub API, cache and static-file binding.
import assert from "node:assert/strict";
import { beforeEach, test } from "node:test";
import worker from "../worker.mjs";

const github = "https://github.com/gehariharan/wasync";
let cache;
let githubCalls;
let githubAnswer;

beforeEach(() => {
  cache = new Map();
  globalThis.caches = {
    default: {
      match: async request => cache.get(request.url)?.clone(),
      put: async (request, response) => { cache.set(request.url, response.clone()); },
    },
  };
  githubCalls = [];
  githubAnswer = () => Response.json([]);
  globalThis.fetch = async (url, init) => {
    githubCalls.push({ url: String(url), init });
    return githubAnswer();
  };
});

function release(version, { published, draft = false, files }) {
  return {
    tag_name: `v${version}`,
    draft,
    prerelease: version.includes("-"),
    published_at: published,
    assets: files.map((suffix, i) => ({ name: `wasync_${version}_${suffix}`, size: 1000 * (i + 1) })),
  };
}

const appFiles = ["darwin_arm64.dmg", "darwin_amd64.dmg", "windows_amd64-setup.exe", "linux_amd64.tar.gz", "linux_arm64.tar.gz"];
// What today's releases look like: no .dmg yet.
const cliFiles = ["darwin_arm64.pkg", "darwin_amd64.pkg", "windows_amd64-setup.exe", "windows_amd64.zip", "linux_amd64.tar.gz", "linux_arm64.tar.gz"];

async function get(path, { method = "GET", env = {} } = {}) {
  const waits = [];
  const assets = { fetch: async request => new Response(`static ${new URL(request.url).pathname}`, { status: 200 }) };
  const response = await worker.fetch(new Request(`https://gehariharan.com${path}`, { method }), { ASSETS: assets, ...env }, {
    waitUntil: promise => waits.push(promise),
  });
  await Promise.all(waits);
  return response;
}

test("the old address and the bare download folder lead to the install options", async () => {
  for (const path of ["/downloads/wasync", "/downloads/wasync/", "/downloads/wasync/mac", "/wasync/download", "/wasync/download/"]) {
    const response = await get(path);
    assert.equal(response.status, 301, path);
    assert.equal(response.headers.get("Location"), "https://gehariharan.com/wasync/installoptions/", path);
  }
});

test("pages and unknown download names are static files", async () => {
  for (const path of ["/wasync/", "/wasync/installoptions/", "/wasync/app.js", "/wasync/download/nope", "/wasync/download/constructor"]) {
    const response = await get(path);
    assert.equal(await response.text(), `static ${path}`, path);
  }
  assert.equal(githubCalls.length, 0);
});

test("before any release has the app, the page hears 404 and downloads open the releases list", async () => {
  githubAnswer = () => Response.json([release("0.1.0-preview.20261126105548", { published: "2026-11-26T11:05:15Z", files: cliFiles })]);
  const info = await get("/wasync/releases.json");
  assert.equal(info.status, 404);
  assert.match((await info.json()).error, /No release has the wasync app yet/);
  const download = await get("/wasync/download/windows-amd64");
  assert.equal(download.status, 302);
  assert.equal(download.headers.get("Location"), `${github}/releases`);
  assert.equal(githubCalls.length, 1, "the empty answer is cached too");
});

test("the newest published release with every app installer is offered", async () => {
  githubAnswer = () => Response.json([
    release("0.2.0", { published: "2026-12-01T00:00:00Z", files: appFiles }),
    release("0.3.0", { published: "2026-12-03T00:00:00Z", draft: true, files: appFiles }),
    release("0.2.1", { published: "2026-12-02T00:00:00Z", files: appFiles.slice(1) }),
    release("0.1.9", { published: "2026-11-30T00:00:00Z", files: [...appFiles, "windows_amd64.zip"] }),
  ]);
  const response = await get("/wasync/releases.json");
  assert.equal(response.status, 200);
  const info = await response.json();
  assert.equal(info.version, "0.2.0");
  assert.equal(info.checksums_url, `${github}/releases/download/v0.2.0/checksums.txt`);
  assert.deepEqual(Object.keys(info.downloads).sort(), ["linux-amd64", "linux-arm64", "mac-amd64", "mac-arm64", "windows-amd64"]);
  assert.deepEqual(info.downloads["mac-arm64"], {
    file: "wasync_0.2.0_darwin_arm64.dmg",
    size: 1000,
    url: `${github}/releases/download/v0.2.0/wasync_0.2.0_darwin_arm64.dmg`,
  });
  const download = await get("/wasync/download/linux-arm64");
  assert.equal(download.status, 302);
  assert.equal(download.headers.get("Location"), `${github}/releases/download/v0.2.0/wasync_0.2.0_linux_arm64.tar.gz`);
  assert.equal(githubCalls.length, 1, "release information is cached");
});

test("optional files are listed when the release has them", async () => {
  githubAnswer = () => Response.json([release("0.2.0", { published: "2026-12-01T00:00:00Z", files: [...appFiles, "windows_amd64.zip", "darwin_arm64.pkg"] })]);
  const info = await (await get("/wasync/releases.json")).json();
  assert.equal(info.downloads["windows-amd64-zip"].file, "wasync_0.2.0_windows_amd64.zip");
  assert.equal(info.downloads["mac-arm64-pkg"].file, "wasync_0.2.0_darwin_arm64.pkg");
  assert.equal(info.downloads["mac-amd64-pkg"], undefined);
});

test("when GitHub fails, the page hears 503 and downloads still reach GitHub", async () => {
  githubAnswer = () => new Response("rate limited", { status: 403 });
  const info = await get("/wasync/releases.json");
  assert.equal(info.status, 503);
  const download = await get("/wasync/download/mac-arm64");
  assert.equal(download.status, 302);
  assert.equal(download.headers.get("Location"), `${github}/releases`);
});

test("only GET and HEAD reach the release routes", async () => {
  const response = await get("/wasync/releases.json", { method: "POST" });
  assert.equal(response.status, 405);
  assert.equal(githubCalls.length, 0);
});
