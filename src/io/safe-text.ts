/**
 * safe-text.ts — minimal sanitization boundary for text that flows from
 * untrusted/semi-trusted sources (PR bodies, agent-produced reasons,
 * candidate problem/rationale) into downstream LLM prompts.
 *
 * This module is INTENTIONALLY small. We do not ship a branded type or
 * compile-time enforcement, because the attacker model for auto-improve
 * is "self + accidental" in a single-operator pipeline. What we DO enforce:
 *   1. Wrap untrusted blocks in a clearly labeled XML-style fence.
 *   2. Escape sequences that commonly reappear as instructions
 *      ("SYSTEM:", "ASSISTANT:", triple backtick fences, HTML-style <system>).
 *   3. Hard-cap length; over-length is truncated with a marker.
 *
 * Consumers MUST pass any external free-text through
 * `sanitizeForPromptEmbedding` before concatenation into a judge/extractor
 * prompt. See docs/design/io-contracts.md §"Prompt embedding safety".
 */

const MAX_INLINE_LEN = 10_000;

const INSTRUCTION_PATTERNS: RegExp[] = [
  /\b(SYSTEM|ASSISTANT|USER)\s*:/gi,
  /<\/?\s*(system|assistant|user|instructions?)\b[^>]*>/gi,
  /^\s*###\s+/gm, // markdown h3 used as section markers in prompts
  /```/g, // fenced code blocks — may be parsed as end-of-system-prompt by some LLMs
];

const REPLACEMENTS = new Map<string, string>([
  ['SYSTEM:', 'SYSTEM\u2009:'], // thin space breaks the token
  ['ASSISTANT:', 'ASSISTANT\u2009:'],
  ['USER:', 'USER\u2009:'],
]);

export interface SanitizeOptions {
  /** Label that appears in the wrapper. Must match `/^[A-Za-z0-9_-]{1,32}$/`. */
  label: string;
  /** Override the default cap (10k chars). Set negative or 0 to disable cap. */
  maxLen?: number;
}

export function sanitizeForPromptEmbedding(raw: string, opts: SanitizeOptions): string {
  if (!/^[A-Za-z0-9_-]{1,32}$/.test(opts.label)) {
    throw new Error(`sanitize label must match /[A-Za-z0-9_-]{1,32}/, got: ${opts.label}`);
  }
  const cap = opts.maxLen ?? MAX_INLINE_LEN;
  let text = raw;

  for (const [from, to] of REPLACEMENTS) {
    text = text.split(from).join(to);
  }
  for (const pat of INSTRUCTION_PATTERNS) {
    text = text.replace(pat, (m) => m.replace(/./g, (c) => (/\s/.test(c) ? c : `${c}\u200b`)));
  }

  let truncated = false;
  if (cap > 0 && text.length > cap) {
    text = text.slice(0, cap);
    truncated = true;
  }

  return (
    `<untrusted-text source="${opts.label}"${truncated ? ' truncated="true"' : ''}>\n` +
    text +
    `\n</untrusted-text source="${opts.label}">`
  );
}

/**
 * Quick detection — returns true if raw text contains instruction-shaped
 * patterns that might attempt prompt injection.
 * Used for logging / metrics, NOT as a security boundary.
 */
export function looksLikePromptInjection(raw: string): boolean {
  return INSTRUCTION_PATTERNS.some((p) => {
    p.lastIndex = 0;
    return p.test(raw);
  });
}
