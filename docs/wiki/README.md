# Wiki Sources

This directory is the source of truth for the [mc-iam-manager GitHub Wiki](https://github.com/m-cmp/mc-iam-manager/wiki).
Edit pages here and open a pull request; after merge, maintainers sync the pages to the wiki.
Direct edits made in the wiki UI are overwritten by the next sync.

## Conventions

- One file per wiki page. The file name (without `.md`) is the page name: `Installation-Guide.md` → `wiki/Installation-Guide`.
- English pages are canonical. Korean translations use the `-ko` suffix (`Installation-Guide-ko.md`) and exist only for core pages.
- Link between pages with wiki-style relative links: `[Initial Setup](Initial-Setup)`.
- Link to source files with absolute URLs on the `main` branch: `https://github.com/m-cmp/mc-iam-manager/blob/main/<path>`.
- Write environment variables with their full names (`MC_IAM_MANAGER_PORT`, never `PORT`).
- Use Mermaid code blocks for diagrams; do not commit image files for diagrams.
- `_Sidebar.md` and `_Footer.md` are rendered on every page. Add new pages to `_Sidebar.md`.
- This `README.md` is not synced.

## Syncing to the Wiki

```bash
# Preview changes (default: dry run)
./scripts/sync-wiki.sh

# Push to https://github.com/m-cmp/mc-iam-manager.wiki.git
./scripts/sync-wiki.sh --apply
```

The script clones the wiki repository into a temporary directory, mirrors `docs/wiki/*.md` (except this README), removes pages that no longer exist here, and pushes a single commit.
