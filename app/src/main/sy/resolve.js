// Resolution of the `sy` binary (C11): SY_BIN (unpackaged only, given as deps.syBin), else
// `sy` on PATH and then ~/.local/bin/sy (%USERPROFILE%\.local\bin\sy.exe) when `sy --version`
// is at least minSy, else the bundled copy at <resourcesPath>/sy/sy[.exe]. The bundled copy is
// never installed onto PATH.

/** Parse the first x.y.z in text; null when none. */
export function parseVersion(text) {
  const m = /(\d+)\.(\d+)\.(\d+)/.exec(String(text ?? ''));
  return m ? [Number(m[1]), Number(m[2]), Number(m[3])] : null;
}

/** a >= b for [x, y, z] triples. */
export function versionAtLeast(a, b) {
  for (let i = 0; i < 3; i++) {
    if (a[i] !== b[i]) return a[i] > b[i];
  }
  return true;
}

/**
 * @param {{env: object, fs: object, path: object, platform: string, resourcesPath?: string,
 *          minSy: string, syBin?: string|null,
 *          version: (bin: string) => Promise<string|null>}} deps
 *   version: runs `<bin> --version` and returns its stdout, or null on failure
 * @returns {Promise<{bin: string, source: 'env'|'path'|'local'|'bundled', version: string|null}|null>}
 */
export async function resolve(deps) {
  const { env, fs, path, platform } = deps;
  const win = platform === 'win32';
  const exe = win ? 'sy.exe' : 'sy';
  const min = parseVersion(deps.minSy) ?? [0, 0, 0];
  const isFile = async (p) => {
    try {
      return (await fs.stat(p)).isFile();
    } catch {
      return false;
    }
  };

  if (deps.syBin) {
    return (await isFile(deps.syBin)) ? { bin: deps.syBin, source: 'env', version: null } : null;
  }

  const candidates = [];
  const pathVar = (win ? env.Path ?? env.PATH : env.PATH) ?? '';
  for (const dir of pathVar.split(win ? ';' : ':')) {
    if (dir && path.isAbsolute(dir)) candidates.push({ bin: path.join(dir, exe), source: 'path' });
  }
  const home = win ? env.USERPROFILE ?? env.HOME : env.HOME ?? env.USERPROFILE;
  if (home) candidates.push({ bin: path.join(home, '.local', 'bin', exe), source: 'local' });

  for (const c of candidates) {
    if (!(await isFile(c.bin))) continue;
    const out = await deps.version(c.bin);
    const v = parseVersion(out);
    if (v && versionAtLeast(v, min)) return { ...c, version: v.join('.') };
  }

  if (deps.resourcesPath) {
    const bundled = path.join(deps.resourcesPath, 'sy', exe);
    if (await isFile(bundled)) return { bin: bundled, source: 'bundled', version: null };
  }
  return null;
}
