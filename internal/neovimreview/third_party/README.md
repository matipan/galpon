# Vendored Neovim review dependencies

These files let `galpon review setup` work without network access. Galpon embeds
source archives and builds the two Tree-sitter parsers on the local machine. It
does not vendor generated shared objects. The directory is named `third_party`,
not `vendor`, because Go module archives omit vendored package directories.
These embedded files must also be available to `go install ...@latest`.

| Component | Upstream pin | Vendored file | SHA-256 |
| --- | --- | --- | --- |
| [render-markdown.nvim](https://github.com/MeanderingProgrammer/render-markdown.nvim) | `f422cb5c6855f150e2ddcfaf44e7157b98b34f6a` | `render-markdown.tar.gz` | `d0721b7bd73e03bf531be679a4111cb0f958c4bfaba190a52e658727cb1a4425` |
| [mini.icons](https://github.com/echasnovski/mini.icons) | `e56797f90192d81f1fda02e662fc3e8e3d775027` | `mini-icons.tar.gz` | `5577366a3174afdae2ba8baa1f1361afaae19cc13de87f899835e4ae47dcbf7e` |
| [tree-sitter-markdown](https://github.com/tree-sitter-grammars/tree-sitter-markdown) | `a0a00f817d02412bd92c54d316f164d827b57b5c` | `tree-sitter-markdown.tar.gz` | `a712569a59f127fd44a1cb59eecdb05d63fa2ccb4622b7d822bff199bf6fb133` |
| [nvim-treesitter](https://github.com/nvim-treesitter/nvim-treesitter) Markdown queries | `9a168f6357ed21c3a636e1727bc7d382abc451b8` | `queries/**` | Per-file values below |

The plugin and grammar licenses are in their source archives and are copied to
the prepared runtime. `queries/LICENSE` is the Apache-2.0 license from the
pinned nvim-treesitter source and is also copied to the prepared runtime.

## Query checksums

```text
f9cb82eb75c5d3c6946903bb2318278d3ca378e507b14569d2bb8af8bba69b95  queries/markdown/folds.scm
7b71d4994cf3968e16d033ac59d28bb034427dd41be6a057b7a2985e2dfbe5b8  queries/markdown/highlights.scm
52550be38883524e3eb393982cbc3e0ff08e7bcfb8ae6e42152eeec531409faa  queries/markdown/indents.scm
34090de78df99b5ea84c2fbb9324f2b7c32e602043595286e83f1a1ac126073c  queries/markdown/injections.scm
7db9147da61de7491ede9155e554dc033d6a1b86ecd1b21f6849c018be9ac875  queries/markdown_inline/highlights.scm
051f44218e0fe25cd949dfccad80699d5456ee379c7c4cfa18a5165776ecab0e  queries/markdown_inline/injections.scm
```
