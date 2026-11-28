# wasync website

The pages at `https://gehariharan.com/wasync/`:

- `/wasync/` is the landing page. Three tiles (Mac, Windows, Linux) with the
  visitor's own system already selected, and its download button under them.
  On a Mac whose browser does not report the chip (Safari, Firefox), the Mac tile
  offers Apple silicon and Intel. Phones get a "send the link to my computer"
  button instead. Below the four feature notes, app screenshots from
  `public/wasync/img/` (WebP in light and dark, with a PNG fallback) show the
  app with sample chats.
- `/wasync/installoptions/` lists every download with its size, install steps,
  Homebrew, system requirements, checksums and the full "wasync is not WhatsApp"
  note.

A dedicated Cloudflare Worker serves only these paths on the existing domain and
its `www` host; the rest of the site is served by its own Worker. The Worker
(`worker.mjs`) answers:

| Path | Answer |
| --- | --- |
| `/wasync/releases.json` | The newest GitHub release that has every app installer (both Mac `.dmg` files, the Windows Setup `.exe`, both Linux `.tar.gz`), with file names and sizes. 404 until such a release exists; the pages then say the first release is on its way. |
| `/wasync/download/<key>` | A redirect to that file in the release, for example `mac-arm64`, `windows-amd64` or `linux-amd64`. Without a release it opens the GitHub releases list. |
| `/wasync/download`, `/downloads/wasync[/...]` | A permanent redirect to `/wasync/installoptions/`. |

Everything else is a static file from `public/`. Release information comes from
the public GitHub Releases API and is cached for two minutes, so new releases
appear without redeploying. No credentials, WhatsApp sessions, archives or user
data are used by this website.

The pages use the app's own design system. `npm run design` copies `tokens.css`,
`components.css`, the bundled Inter font and the logo from
`internal/api/webassets/` into `public/wasync/ui/` (git-ignored), and every dev,
check and deploy command runs it first. Edit those files in the app, never here.

The platform tiles show the Apple and Windows logos and Tux, and the install
options page names their owners. Apple's and Microsoft's trademark guidelines ask
for permission to use their logos; to switch a tile to a neutral icon, replace
its `svg.logo` in `public/wasync/index.html`.

## Develop and test

Node.js 22.23+, 24.22+ or 26.12+, as the `cf` CLI requires (24 recommended).
From `website/`:

```sh
npm ci
npm test        # the Worker's routes, the platform detection and the screenshots
npm run dev     # serves http://localhost:8787/wasync/ with the real Worker
npm run check   # builds exactly what a deploy uploads, and uploads nothing
```

`npm run dev` asks GitHub for release information like the live site does.

## Publish

The GitHub Actions workflow `.github/workflows/deploy-website.yml` publishes the
site with the `cf` CLI pinned in `package.json`. It runs on every push to `main`
that changes `website/` or the design files it copies from
`internal/api/webassets/`, and on demand from the Actions tab (Run workflow). It
installs from the lockfile, runs the tests, deploys, then checks that both pages
answer.

It needs two repository secrets on `gehariharan/wasync`:

- `CLOUDFLARE_API_TOKEN`: a Cloudflare API token for the account that owns
  `gehariharan.com`, with these permissions and no others:
  - Account: Workers Scripts, Edit
  - Zone `gehariharan.com`: Workers Routes, Edit
  - Zone `gehariharan.com`: Zone, Read
- `CLOUDFLARE_ACCOUNT_ID`: that account's ID.

Until both exist, the workflow stops after its first step with a notice saying
nothing was deployed; the run does not fail.

To publish by hand instead, from a clean clone of `main` in a shell where both
values are set as environment variables:

```sh
cd website
npm ci
npm run check
npm run deploy
curl -sI https://gehariharan.com/wasync/
```

Never commit tokens, `.cloudflare/` or `.wrangler/` state. Application releases
use their own workflows; website changes never start one.
