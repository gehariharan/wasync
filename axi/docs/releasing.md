# Releasing wasync-axi

AXI now ships with wasync from the monorepo. See the
[common release guide](../../docs/releasing.md).

The local packaging helpers remain independently runnable from `axi/`:

```sh
scripts/package.sh --dry-run 0.0.0-ci
scripts/check-dist.sh 0.0.0-ci
```

The root workflow owns publication, signing and Homebrew updates.
