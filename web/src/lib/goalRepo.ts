// goalRepo recognises a repository named in a mission goal and matches
// it against a connector's repository list, purely client-side (issue
// #563): a proposal the operator sees and confirms, never resolved or
// invented at run time. No backend involvement.
import type { GitHubRepo } from '../api/types'

// A slug segment: word characters, dots and hyphens, but never
// starting or ending on a dot/hyphen (so trailing sentence punctuation
// like "octocat/hello-world." never gets swallowed into the name).
const SLUG = '[A-Za-z0-9_](?:[A-Za-z0-9_.-]*[A-Za-z0-9_])?'

// URL forms: https://github.com/owner/repo(.git) and the bare
// github.com/owner/repo, both anchored on a repo segment with no
// trailing path (a URL pointing deeper, e.g. .../repo/issues/1, is
// treated as naming that repo too since the first two segments after
// the host are always owner/repo).
const urlPattern = new RegExp(`github\\.com/(${SLUG})/(${SLUG})(?:\\.git)?(?:[/?#]|\\b)`, 'gi')

// owner/repo pair, not preceded/followed by another slash (so a
// three-segment path like a/b/c never matches) and not looking like a
// file path (an extension-bearing final segment, e.g. src/index.ts).
const pairPattern = new RegExp(`(?<![\\w./-])(${SLUG})/(${SLUG})(?![\\w/-])`, 'g')

// A bare repo-like token is only trusted as a repo mention when it sits
// immediately next to the word "repo"/"repository": otherwise plain
// prose has far too many false positives (any hyphenated word,
// filenames, etc). commonWords excludes ordinary sentence filler
// ("Repo lives at...", "The repository needs...") that would otherwise
// match the adjacent-word slot.
const commonWords = new Set([
  'a', 'an', 'the', 'this', 'that', 'these', 'those', 'is', 'are', 'was',
  'were', 'lives', 'located', 'found', 'here', 'there', 'at', 'in', 'on',
  'for', 'and', 'or', 'to', 'its', 'it', 'my', 'our', 'your', 'named',
])
// Checked in this order per keyword occurrence: the word AFTER
// repo/repository is preferred (afterWordPattern), since the leading
// alternative below can otherwise swallow a common word like "The"
// right before the keyword and hide a real name that follows it.
const afterWordPattern = new RegExp(`\\b(?:repo|repository)\\s+(${SLUG})(?:$|\\s)`, 'gi')
const beforeWordPattern = new RegExp(`(?:^|\\s)(${SLUG})\\s+(?:repo|repository)\\b`, 'gi')

// looksLikeFilePath rejects a candidate whose repo segment carries a
// file extension (index.ts, README.md, ...): those are far more likely
// a path fragment than a repository name.
function looksLikeFilePath(segment: string): boolean {
  return /\.[A-Za-z0-9]{1,8}$/.test(segment) && !/\.git$/i.test(segment)
}

export interface RepoMention {
  owner?: string
  name: string
}

// extractRepoMentions scans goal text for repository references in
// rough order of confidence: full URLs and owner/repo pairs first
// (both name an explicit owner), then bare names called out next to
// "repo"/"repository". Dedupes case-insensitively on owner+name.
export function extractRepoMentions(goal: string): RepoMention[] {
  const mentions: RepoMention[] = []
  const seen = new Set<string>()
  const add = (owner: string | undefined, name: string) => {
    const cleanName = name.replace(/\.git$/i, '')
    if (!cleanName || looksLikeFilePath(cleanName)) return
    const key = `${(owner ?? '').toLowerCase()}/${cleanName.toLowerCase()}`
    if (seen.has(key)) return
    seen.add(key)
    mentions.push(owner ? { owner, name: cleanName } : { name: cleanName })
  }

  for (const m of goal.matchAll(urlPattern)) {
    if (m[1] && m[2]) add(m[1], m[2])
  }
  for (const m of goal.matchAll(pairPattern)) {
    if (looksLikeFilePath(m[2])) continue
    add(m[1], m[2])
  }
  for (const m of goal.matchAll(afterWordPattern)) {
    if (m[1] && !commonWords.has(m[1].toLowerCase())) add(undefined, m[1])
  }
  for (const m of goal.matchAll(beforeWordPattern)) {
    if (m[1] && !commonWords.has(m[1].toLowerCase())) add(undefined, m[1])
  }

  return mentions
}

// levenshtein computes edit distance between two strings (simple DP,
// goal text is short so this never needs to be fast).
function levenshtein(a: string, b: string): number {
  const dp: number[][] = Array.from({ length: a.length + 1 }, (_, i) => [
    i,
    ...Array(b.length).fill(0),
  ])
  for (let j = 0; j <= b.length; j++) dp[0][j] = j
  for (let i = 1; i <= a.length; i++) {
    for (let j = 1; j <= b.length; j++) {
      dp[i][j] =
        a[i - 1] === b[j - 1]
          ? dp[i - 1][j - 1]
          : 1 + Math.min(dp[i - 1][j - 1], dp[i - 1][j], dp[i][j - 1])
    }
  }
  return dp[a.length][b.length]
}

export type RepoMatch = { repo: GitHubRepo; guess: boolean } | { candidates: GitHubRepo[] } | null

// matchRepo resolves a mention list against a connector's repo list:
// an exact full_name match (owner present) or exact name match (owner
// absent) wins outright with guess=false. Otherwise, for a bare name,
// a case-insensitive prefix or small edit-distance match is tried:
// exactly one candidate proposes it as a guess, several return as
// candidates for the operator to pick, none returns null.
export function matchRepo(mentions: RepoMention[], repos: GitHubRepo[]): RepoMatch {
  for (const mention of mentions) {
    const name = mention.name.toLowerCase()
    if (mention.owner) {
      const fullName = `${mention.owner}/${mention.name}`.toLowerCase()
      const exact = repos.find((r) => r.full_name.toLowerCase() === fullName)
      if (exact) return { repo: exact, guess: false }
      continue
    }

    const exact = repos.find((r) => r.full_name.split('/')[1]?.toLowerCase() === name)
    if (exact) return { repo: exact, guess: false }

    const candidates = repos.filter((r) => {
      const repoName = (r.full_name.split('/')[1] ?? '').toLowerCase()
      if (!repoName) return false
      if (repoName.startsWith(name) || name.startsWith(repoName)) return true
      return levenshtein(repoName, name) <= 2
    })
    if (candidates.length === 1) return { repo: candidates[0], guess: true }
    if (candidates.length > 1) return { candidates }
  }
  return null
}
