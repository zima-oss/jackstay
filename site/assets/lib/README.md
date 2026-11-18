# Vendored scripts

What the site serves from here, as it was published, unchanged.

| File | What | Source | Licence |
|---|---|---|---|
| `flexsearch-0.8.143/flexsearch.bundle.min.js` | FlexSearch 0.8.143, the search behind the site's search box (the version the Hextra theme names) | `dist/` of the npm package `flexsearch@0.8.143`, whose registry entry gives the integrity `sha512-J7TiCTSdtqSdmd723Bk9Wj5se2gGLLpRwujQztOm45n24VlW53O3H1GpWK/ZslOPB/WzQxwSGDaQ8Pm8RQW89Q==`; the file's SHA-256 is `433e941a8a573ebb9931fc16fc75266ab6b93f569ac2fb4f3dc66882e0416f4c` | Apache-2.0, copyright Thomas Wilkerling, as the file's header says; the licence's text is the repository's [LICENSE](../../../LICENSE) |

To update it: take the version the pinned Hextra names (`flexsearch.version` in its
`layouts/_partials/scripts/search.html`), download the package's tarball from the npm registry,
check it against the registry's integrity value, copy `dist/flexsearch.bundle.min.js` into a folder
named for the version, and change `params.search.flexsearch.js` in `site/hugo.yaml` and this table.
