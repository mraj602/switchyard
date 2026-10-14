// The runner (C11): child_process.execFile (never a shell) with the cleaned environment, a
// timeout per purpose, stdout capped at 1 MiB and stderr at 64 KiB. Only main/sy/ imports
// child_process. A non-zero exit becomes {ok: false, exit, error: <first stderr line>}.

import * as childProcess from 'node:child_process';
import { SY_STDOUT_MAX, SY_STDERR_MAX } from '../../shared/constants.js';
import { cleanEnv } from './env.js';

const firstLine = (s) => String(s ?? '').split(/\r?\n/).find((l) => l.trim() !== '')?.trim().slice(0, 300) ?? '';

/**
 * @param {{execFile?: Function, env: object, platform?: string}} deps
 * @returns {(bin: string, argv: string[], opts: {stdin?: string|null, timeout: number}) =>
 *   Promise<{ok: boolean, exit: number, stdout: string, error?: string}>}
 */
export function createRunner(deps) {
  const execFile = deps.execFile ?? childProcess.execFile;
  const platform = deps.platform ?? process.platform;
  return function run(bin, argv, opts) {
    return new Promise((resolve) => {
      let settled = false;
      const done = (r) => {
        if (!settled) {
          settled = true;
          resolve(r);
        }
      };
      let child;
      try {
        child = execFile(
          bin,
          argv,
          { env: cleanEnv(deps.env, platform), timeout: opts.timeout, maxBuffer: SY_STDOUT_MAX, windowsHide: true, encoding: 'utf8', shell: false },
          (err, stdout, stderr) => {
            const out = String(stdout ?? '').slice(0, SY_STDOUT_MAX);
            const errText = String(stderr ?? '').slice(0, SY_STDERR_MAX);
            if (!err) return done({ ok: true, exit: 0, stdout: out });
            let exit = typeof err.code === 'number' ? err.code : -1;
            let error = firstLine(errText);
            if (err.code === 'ENOENT') error = 'sy not found';
            else if (String(err.code).includes('MAXBUFFER')) error = 'sy output too large';
            else if (err.killed || err.signal) error ||= `sy timed out or was stopped (${err.signal ?? 'killed'})`;
            if (!error) error = `sy exited with ${exit}`;
            if (typeof exit !== 'number') exit = -1;
            return done({ ok: false, exit, stdout: out, error });
          },
        );
      } catch (err) {
        done({ ok: false, exit: -1, stdout: '', error: String(err?.message ?? err).split('\n')[0] });
        return;
      }
      if (child?.stdin) {
        child.stdin.on?.('error', () => {}); // EPIPE when sy exits early: the exit status tells
        child.stdin.end(opts.stdin ?? '');
      }
    });
  };
}
