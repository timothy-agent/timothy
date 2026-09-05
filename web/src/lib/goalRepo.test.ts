import { describe, expect, it } from 'vitest'
import type { GitHubRepo } from '../api/types'
import { extractRepoMentions, matchRepo } from './goalRepo'

function repo(fullName: string): GitHubRepo {
  return {
    full_name: fullName,
    private: false,
    default_branch: 'main',
    html_url: `https://github.com/${fullName}`,
    clone_url: `https://github.com/${fullName}.git`,
    pushed_at: '2026-08-01T00:00:00Z',
  }
}

describe('extractRepoMentions', () => {
  it('recognizes a full github.com URL with .git', () => {
    expect(extractRepoMentions('Clone https://github.com/octocat/hello-world.git and build it')).toEqual([
      { owner: 'octocat', name: 'hello-world' },
    ])
  })

  it('recognizes a full github.com URL without .git', () => {
    expect(extractRepoMentions('See https://github.com/octocat/hello-world for details')).toEqual([
      { owner: 'octocat', name: 'hello-world' },
    ])
  })

  it('recognizes a bare github.com/owner/repo without scheme', () => {
    expect(extractRepoMentions('Repo lives at github.com/octocat/hello-world')).toEqual([
      { owner: 'octocat', name: 'hello-world' },
    ])
  })

  it('recognizes a bare owner/repo pair', () => {
    expect(extractRepoMentions('Audit octocat/hello-world for dependency issues')).toEqual([
      { owner: 'octocat', name: 'hello-world' },
    ])
  })

  it('recognizes a bare repo name next to the word "repo"', () => {
    expect(
      extractRepoMentions('Clone the solid-principles-example-laravel repo and audit its dependencies'),
    ).toEqual([{ name: 'solid-principles-example-laravel' }])
  })

  it('recognizes a bare repo name preceding the word "repository"', () => {
    expect(extractRepoMentions('The repository mumu needs a dependency bump')).toEqual([{ name: 'mumu' }])
  })

  it('does not treat a file path with an extension as an owner/repo pair', () => {
    expect(extractRepoMentions('Update src/index.ts to fix the bug')).toEqual([])
  })

  it('does not treat a bare word as a mention without "repo"/"repository" nearby', () => {
    expect(extractRepoMentions('Refactor the payment-service module for clarity')).toEqual([])
  })

  it('ignores a three-segment path (not owner/repo)', () => {
    expect(extractRepoMentions('Look at a/b/c for reference')).toEqual([])
  })

  it('dedupes repeated mentions of the same repo', () => {
    expect(
      extractRepoMentions('Clone octocat/hello-world. Then push back to octocat/hello-world when done.'),
    ).toEqual([{ owner: 'octocat', name: 'hello-world' }])
  })

  it('returns an empty list for goal text with no repo mention', () => {
    expect(extractRepoMentions('Summarize last week and email me the digest')).toEqual([])
  })
})

describe('matchRepo', () => {
  const repos = [repo('octocat/hello-world'), repo('octocat/secret-project'), repo('acme/widget-factory')]

  it('matches exactly on full_name (owner + name) with guess=false', () => {
    expect(matchRepo([{ owner: 'octocat', name: 'hello-world' }], repos)).toEqual({
      repo: repos[0],
      guess: false,
    })
  })

  it('matches exactly on a bare name with guess=false', () => {
    expect(matchRepo([{ name: 'widget-factory' }], repos)).toEqual({ repo: repos[2], guess: false })
  })

  it('is case-insensitive on both owner/repo and bare-name matches', () => {
    expect(matchRepo([{ owner: 'OctoCat', name: 'Hello-World' }], repos)).toEqual({
      repo: repos[0],
      guess: false,
    })
    expect(matchRepo([{ name: 'WIDGET-FACTORY' }], repos)).toEqual({ repo: repos[2], guess: false })
  })

  it('returns null for an owner/repo pair naming an owner not in the list', () => {
    expect(matchRepo([{ owner: 'someoneelse', name: 'hello-world' }], repos)).toBeNull()
  })

  it('proposes a single prefix match as a guess', () => {
    expect(matchRepo([{ name: 'hello' }], repos)).toEqual({ repo: repos[0], guess: true })
  })

  it('proposes a single close edit-distance match as a guess', () => {
    expect(matchRepo([{ name: 'hello-worId' }], repos)).toEqual({ repo: repos[0], guess: true })
  })

  it('returns candidates when two or more repos match a bare name', () => {
    const ambiguous = [repo('octocat/widget-one'), repo('octocat/widget-two')]
    expect(matchRepo([{ name: 'widget' }], ambiguous)).toEqual({ candidates: ambiguous })
  })

  it('returns null when no repo is close enough to a bare name', () => {
    expect(matchRepo([{ name: 'completely-unrelated-name' }], repos)).toBeNull()
  })

  it('returns null for an empty mention list', () => {
    expect(matchRepo([], repos)).toBeNull()
  })

  it('tries later mentions when an earlier one has no match', () => {
    expect(
      matchRepo([{ owner: 'nope', name: 'nothing' }, { name: 'widget-factory' }], repos),
    ).toEqual({ repo: repos[2], guess: false })
  })
})
