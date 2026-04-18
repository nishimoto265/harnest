import pino, { type DestinationStream, type Logger, type LoggerOptions } from 'pino';

export type { Logger };

/**
 * Create a pino logger.
 *
 * - Logs go to **stderr** by default, so commands that emit machine output on
 *   stdout (e.g. `detect-merged` printing JSON) can be safely piped without
 *   contamination.
 * - If `pretty` is true and stderr is a TTY, use pino-pretty (human-readable).
 * - Otherwise emit newline-delimited JSON on stderr.
 *
 * Levels accepted: trace|debug|info|warn|error|fatal.
 */
export function createLogger(opts: {
  level?: string;
  pretty?: boolean;
  name?: string;
  /** @default "stderr" */
  stream?: 'stdout' | 'stderr';
}): Logger {
  const level = opts.level ?? process.env.LOG_LEVEL ?? 'info';
  const streamName = opts.stream ?? 'stderr';
  const outStream = streamName === 'stdout' ? process.stdout : process.stderr;
  const usePretty = (opts.pretty ?? true) && Boolean(outStream.isTTY);

  const base: LoggerOptions = {
    level,
    base: { pid: process.pid, name: opts.name ?? 'auto-improve' },
    timestamp: pino.stdTimeFunctions.isoTime,
  };

  if (usePretty) {
    return pino({
      ...base,
      transport: {
        target: 'pino-pretty',
        options: {
          colorize: true,
          translateTime: 'SYS:HH:MM:ss.l',
          ignore: 'pid,hostname,name',
          singleLine: false,
          destination: streamName === 'stdout' ? 1 : 2,
        },
      },
    });
  }

  const dest: DestinationStream = pino.destination({
    dest: streamName === 'stdout' ? 1 : 2,
    sync: true,
  });
  return pino(base, dest);
}

/** Cached default logger for modules that want a ready-to-use instance. */
let _defaultLogger: Logger | null = null;
export function getDefaultLogger(): Logger {
  if (!_defaultLogger) {
    _defaultLogger = createLogger({});
  }
  return _defaultLogger;
}
