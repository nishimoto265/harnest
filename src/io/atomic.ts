import {
  closeSync,
  existsSync,
  mkdirSync,
  openSync,
  renameSync,
  unlinkSync,
  writeSync,
} from 'node:fs';
import { dirname } from 'node:path';

/**
 * Write `content` to `path` atomically by writing to `<path>.tmp-<pid>-<ms>-<rand>`
 * first and renaming. Same-filesystem renames are atomic on POSIX, so readers
 * never see a half-written file. If writing OR renaming fails, the temp file
 * is removed so the operator is not left with orphaned completion-marker
 * temp files accumulating on disk.
 *
 * Callers rely on the absence of the final path as the signal "not yet completed"
 * (see io-contracts.md: "Completion marker"). DO NOT bypass this helper for
 * step-completion-marker files (manifest.json, task-package.json, decision.json).
 */
export function writeAtomic(path: string, content: string | Buffer): void {
  const dir = dirname(path);
  mkdirSync(dir, { recursive: true });
  const tmp = `${path}.tmp-${process.pid}-${Date.now()}-${Math.random()
    .toString(36)
    .slice(2, 10)}`;
  const fd = openSync(tmp, 'wx'); // fail fast if collision

  const removeTmp = () => {
    try {
      if (existsSync(tmp)) unlinkSync(tmp);
    } catch {
      /* noop */
    }
  };

  try {
    const buf = typeof content === 'string' ? Buffer.from(content, 'utf8') : content;
    writeSync(fd, buf, 0, buf.length, 0);
  } catch (e) {
    try {
      closeSync(fd);
    } catch {
      /* noop */
    }
    removeTmp();
    throw e;
  }
  try {
    closeSync(fd);
  } catch (e) {
    removeTmp();
    throw e;
  }
  try {
    renameSync(tmp, path);
  } catch (e) {
    // rename can fail on: full disk, permission, cross-device.
    // We unlink the temp so the filesystem is left clean.
    removeTmp();
    throw e;
  }
}
