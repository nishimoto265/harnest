#!/usr/bin/env -S npx tsx
/**
 * auto-improve CLI.
 *
 * M1 subcommands:
 *   preflight       Validate environment & config. Exits non-zero on any NG.
 *   detect-merged   Print JSON list of unprocessed merged PRs to stdout.
 *   help            Show usage.
 *   version         Print version.
 *
 * Future (M2+): run-cycle2, run-cycle3, step10, step20, ...
 */

import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { detectMergedCLI } from '../detect-merged.ts';
import { preflightCLI } from '../preflight.ts';

type Command = (argv: string[]) => Promise<number>;

const USAGE = `auto-improve — self-improving harness pipeline (M1)

USAGE
  pnpm auto-improve <command> [options]

COMMANDS
  preflight        Check environment & config. Non-zero exit on failure.
                   Options: --json (machine-readable; defaults to table on TTY,
                            JSON otherwise)
  detect-merged    Print JSON of merged PRs not yet processed.
                   Options: --skip-preflight (skip the critical-checks gate)
  help             Show this message.
  version          Print package version.

ENV
  AUTO_IMPROVE_REPO_ROOT   Override auto-detected git root.
  AUTO_IMPROVE_CONFIG      Path to config.yaml (default: <repo>/auto-improve/config.yaml).
  LOG_LEVEL                trace|debug|info|warn|error|fatal (default: info).

EXAMPLES
  pnpm auto-improve preflight
  pnpm auto-improve detect-merged | jq '.prs[].number'
`;

const COMMANDS: Record<string, Command> = {
  preflight: async (argv) => preflightCLI(argv),
  'detect-merged': async (argv) => detectMergedCLI(argv),
  help: async () => {
    process.stdout.write(USAGE);
    return 0;
  },
  '--help': async () => {
    process.stdout.write(USAGE);
    return 0;
  },
  '-h': async () => {
    process.stdout.write(USAGE);
    return 0;
  },
  version: async () => {
    process.stdout.write(getVersion() + '\n');
    return 0;
  },
  '--version': async () => {
    process.stdout.write(getVersion() + '\n');
    return 0;
  },
};

function getVersion(): string {
  const here = fileURLToPath(import.meta.url);
  const pkg = resolve(here, '..', '..', '..', 'package.json');
  try {
    const parsed = JSON.parse(readFileSync(pkg, 'utf8')) as {
      version?: string;
    };
    return parsed.version ?? '0.0.0';
  } catch {
    return '0.0.0';
  }
}

async function main(): Promise<number> {
  const [cmd, ...rest] = process.argv.slice(2);
  if (!cmd) {
    process.stderr.write(USAGE);
    return 2;
  }
  const handler = COMMANDS[cmd];
  if (!handler) {
    process.stderr.write(`Unknown command: ${cmd}\n\n${USAGE}`);
    return 2;
  }
  return handler(rest);
}

main()
  .then((code) => process.exit(code))
  .catch((e: unknown) => {
    const msg = e instanceof Error ? (e.stack ?? e.message) : String(e);
    process.stderr.write(`fatal: ${msg}\n`);
    process.exit(1);
  });
