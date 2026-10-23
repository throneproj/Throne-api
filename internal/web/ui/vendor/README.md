# Vendored libraries

Served as they are by `thronectl web`; there is no build step.

| File | Package | Source file |
|---|---|---|
| `preact.js` | preact 10.29.8 | `dist/preact.module.js` |
| `hooks.js` | preact 10.29.8 | `hooks/dist/hooks.module.js`, its `from"preact"` import rewritten to `from"./preact.js"` |
| `htm.js` | htm 3.1.1 | `dist/htm.module.js` |

The `//# sourceMappingURL` lines were removed. The tarballs came from registry.npmjs.org and
matched the registry's integrity hashes:

- preact-10.29.8.tgz `sha512-ej2aVZ+vZ8WO7tvlQWRM9N63A0KzF9q4mWJfDUHgYaIofWY9hu74QdnQrjoPMmZi2/nZ5gN0bJCQF49xQqx09Q==`
- htm-3.1.1.tgz `sha512-983Vyg8NwUE7JkZ6NmOqpCZ+sh1bKv2iYTlUkzlWmA5JD2acKoxd4KVxbMmxX/85mtfdnDmTFoNKcg5DGAvxNQ==`

preact is MIT licensed (LICENSE-preact), htm Apache-2.0 licensed (LICENSE-htm).
