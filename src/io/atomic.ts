import { closeSync, existsSync, mkdirSync, openSync, renameSync, unlinkSync, writeSync } from 'node:fs';
import { dirname } from 'node:path';

/**
 * Write `content` to `path` atomically by writing to `<path>.tmp-<pid>-<rand>`
 * first and renaming. Same-filesystem renames are atomic on POSIX, so readers
 * never see a half-written file. Any partial `.tmp-*` left from a crash is
 * abandoned; callers must rely on the absence of the final path as the signal
 * "not yet completed" (see io-contracts.md: "Completion marker").
 *
 * DO NOT bypass this helper for step-completion-marker files
 * (manifest.json, task-package.json, decision.json, etc.).
 */
export function writeAtomic(path: string, content: string | Buffer): void {
  const dir = dirname(path);
  mkdirSync(dir, { recursive: true });
  const tmp = `${path}.tmp-${process.pid}-${Math.random().toString(36).slice(2, 10)}`;
  const fd = openSync(tmp, 'wx'); // fail if tmp collision
  try {
    const buf = typeof content === 'string' ? Buffer.from(content, 'utf8') : content;
    writeSync(fd, buf, 0, buf.length, 0);
  } catch (e) {
    try {
      closeSync(fd);
    } catch {
      /* noop */
    }
    try {
      if (existsSync(tmp)) unlinkSync(tmp);
    } catch {
      /* noop */
    }
    throw e;
  }
  closeSync(fd);
  renameSync(tmp, path);
}
