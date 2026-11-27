// Worker for gehariharan.com/wasync. The pages are static files in ./public:
// /wasync/ is the landing page and /wasync/installoptions/ lists every download.
// This code answers only:
//   /wasync/releases.json       the newest release that has the app installers
//   /wasync/download/<key>      a redirect to that release's file on GitHub
//   /wasync/download, /downloads/wasync[/...]   a redirect to the install options
const repository = "gehariharan/wasync";
const github = `https://github.com/${repository}`;
const base = "/wasync";
const versionPattern = /^v([0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9][A-Za-z0-9.-]*)?)$/;

// Page key -> release file suffix. A release is offered only when it has every
// required file; the others are listed when present.
const assets = {
  "mac-arm64": { suffix: "darwin_arm64.dmg", required: true },
  "mac-amd64": { suffix: "darwin_amd64.dmg", required: true },
  "windows-amd64": { suffix: "windows_amd64-setup.exe", required: true },
  "linux-amd64": { suffix: "linux_amd64.tar.gz", required: true },
  "linux-arm64": { suffix: "linux_arm64.tar.gz", required: true },
  "windows-amd64-zip": { suffix: "windows_amd64.zip" },
  "mac-arm64-pkg": { suffix: "darwin_arm64.pkg" },
  "mac-amd64-pkg": { suffix: "darwin_amd64.pkg" },
};
const required = Object.values(assets).filter(asset => asset.required);

function describe(releases) {
  // GitHub's list ordering can differ for prereleases; choose by publication.
  const release = [...releases]
    .sort((a, b) => (b.published_at || "").localeCompare(a.published_at || ""))
    .find(item => {
      const match = versionPattern.exec(item.tag_name || "");
      if (item.draft || !match) return false;
      return required.every(a => item.assets?.some(asset => asset.name === `wasync_${match[1]}_${a.suffix}`));
    });
  if (!release) return null;
  const version = versionPattern.exec(release.tag_name)[1];
  const downloads = {};
  for (const [key, a] of Object.entries(assets)) {
    const file = `wasync_${version}_${a.suffix}`;
    const asset = release.assets.find(item => item.name === file);
    if (asset) downloads[key] = { file, size: asset.size, url: `${github}/releases/download/v${version}/${file}` };
  }
  return {
    version,
    published_at: release.published_at,
    html_url: `${github}/releases/tag/v${version}`,
    checksums_url: `${github}/releases/download/v${version}/checksums.txt`,
    downloads,
  };
}

// The release description, or null while no release has the app installers.
// Both answers are cached for two minutes, so GitHub sees at most one request
// per data centre in that time.
async function releaseInfo(request, context) {
  const cacheURL = new URL(`${base}/releases.json`, request.url);
  cacheURL.hostname = "gehariharan.com";
  const cacheKey = new Request(cacheURL);
  const cached = await caches.default.match(cacheKey);
  if (cached) return (await cached.json()).release;
  const headers = { Accept: "application/vnd.github+json", "User-Agent": "wasync-site" };
  const response = await fetch(`https://api.github.com/repos/${repository}/releases?per_page=20`, {
    headers, signal: AbortSignal.timeout(10000),
  });
  if (!response.ok) throw new Error("Release service is temporarily unavailable");
  const release = describe(await response.json());
  context.waitUntil(caches.default.put(cacheKey, Response.json({ release }, {
    headers: { "Cache-Control": "public, max-age=120" },
  })));
  return release;
}

export default {
  async fetch(request, env, context) {
    const url = new URL(request.url);
    if (url.pathname === "/downloads/wasync" || url.pathname.startsWith("/downloads/wasync/") ||
        url.pathname === `${base}/download` || url.pathname === `${base}/download/`) {
      return Response.redirect(new URL(`${base}/installoptions/`, url).toString(), 301);
    }
    const key = url.pathname.startsWith(`${base}/download/`) ? url.pathname.slice(`${base}/download/`.length) : null;
    const download = key !== null && Object.hasOwn(assets, key);
    if (url.pathname !== `${base}/releases.json` && !download) return env.ASSETS.fetch(request);
    if (!["GET", "HEAD"].includes(request.method)) {
      return new Response("Method not allowed", { status: 405, headers: { Allow: "GET, HEAD" } });
    }
    try {
      const info = await releaseInfo(request, context);
      if (download) {
        const target = info?.downloads[key]?.url || `${github}/releases`;
        return new Response(null, { status: 302, headers: { Location: target, "Cache-Control": "public, max-age=60" } });
      }
      if (!info) {
        return Response.json({ error: "No release has the wasync app yet", html_url: `${github}/releases` }, {
          status: 404, headers: { "Cache-Control": "public, max-age=60" },
        });
      }
      return new Response(request.method === "HEAD" ? null : JSON.stringify(info), {
        headers: { "Content-Type": "application/json; charset=utf-8", "Cache-Control": "public, max-age=120" },
      });
    } catch {
      if (download) return Response.redirect(`${github}/releases`, 302);
      return Response.json({ error: "Release information is temporarily unavailable", html_url: `${github}/releases` }, { status: 503 });
    }
  },
};
