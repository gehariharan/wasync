// Cloudflare configuration for the wasync site, deployed with `npm run deploy`
// (the cf CLI). Pages are static files in ./public (see wrangler.config.ts);
// worker.mjs answers the routes listed in runWorkerFirst.
import { bindings, defineConfig, triggers } from "cf/config";

export default defineConfig({
	worker: {
		name: "wasync-site",
		compatibilityDate: "2026-09-30",
		entrypoint: "worker.mjs",
		workersDev: false,
		assets: {
			runWorkerFirst: [
				"/wasync/releases.json",
				"/wasync/download",
				"/wasync/download/*",
				"/downloads/wasync",
				"/downloads/wasync/*",
			],
		},
		triggers: [
			triggers.fetch({
				pattern: "gehariharan.com/wasync",
				zone: "gehariharan.com",
			}),
			triggers.fetch({
				pattern: "gehariharan.com/wasync/*",
				zone: "gehariharan.com",
			}),
			triggers.fetch({
				pattern: "www.gehariharan.com/wasync",
				zone: "gehariharan.com",
			}),
			triggers.fetch({
				pattern: "www.gehariharan.com/wasync/*",
				zone: "gehariharan.com",
			}),
			triggers.fetch({
				pattern: "gehariharan.com/downloads/wasync",
				zone: "gehariharan.com",
			}),
			triggers.fetch({
				pattern: "gehariharan.com/downloads/wasync/*",
				zone: "gehariharan.com",
			}),
			triggers.fetch({
				pattern: "www.gehariharan.com/downloads/wasync",
				zone: "gehariharan.com",
			}),
			triggers.fetch({
				pattern: "www.gehariharan.com/downloads/wasync/*",
				zone: "gehariharan.com",
			}),
		],
		env: {
			ASSETS: bindings.assets(),
		},
	},
});
