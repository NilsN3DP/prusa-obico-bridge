import { cp, mkdir, readFile, writeFile } from 'node:fs/promises';
import { compile } from 'svelte/compiler';

const directory = new URL('./dist/ui/', import.meta.url);
await mkdir(directory, { recursive: true });
const source = await readFile(new URL('./BridgePage.svelte', import.meta.url), 'utf8');
const { js, warnings } = compile(source, {
  filename: 'BridgePage.svelte',
  generate: 'client',
  runes: true,
});
if (warnings.length) throw new Error(warnings.map(warning => warning.message).join('\n'));
await writeFile(new URL('BridgePage.js', directory), js.code);
await cp(new URL('./index.js', import.meta.url), new URL('index.js', directory));
await cp(new URL('./locales/', import.meta.url), new URL('locales/', directory), { recursive: true });
await cp(new URL('../module.yaml', import.meta.url), new URL('../module.yaml', directory));
