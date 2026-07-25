# Cutting a release

The `curl | sh` installer downloads prebuilt binaries from a **GitHub Release**.
Until a release exists, `install.sh` will fail with "has a release been published
yet?" — so this is the one manual step to make one-command install work.

## Steps

```bash
# 1. Cross-compile every platform (asset names match what install.sh expects)
scripts/build-release.sh v0.1.0        # → dist/signoz-init_<os>_<arch>

# 2a. With the GitHub CLI (easiest)
gh release create v0.1.0 dist/* --title v0.1.0 --generate-notes

# 2b. Or in the browser
#     github.com/Eshan276/signoz_hackathon/releases/new
#     tag v0.1.0, then drag every file from dist/ in as an asset.
```

That's it. After the release is up:

```bash
curl -fsSL https://raw.githubusercontent.com/Eshan276/signoz_hackathon/main/install.sh | sh
signoz-init init ./demo
```

## Notes

- The version tag (`v0.1.0`) is baked into the binary via `-ldflags -X main.version`,
  so `signoz-init --version` reports it.
- `install.sh` resolves `latest` to the newest release, so re-releasing with a new tag
  updates what new users get without editing the install command.
- Binaries are ~7 MB, statically linked (`CGO_ENABLED=0`), no runtime dependencies.
