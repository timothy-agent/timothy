// The git-hosting connector/destination kinds and the per-kind copy the
// UI needs. Centralized because the same label, SSH-key page and repo
// URL shape were repeated as kind ternaries across a dozen call sites,
// where a new kind silently fell through to the GitHub branch.

export const GIT_KINDS = ['github', 'bitbucket', 'gitlab'] as const

export type GitKind = (typeof GIT_KINDS)[number]

export function isGitKind(kind: string): kind is GitKind {
  return (GIT_KINDS as readonly string[]).includes(kind)
}

export interface GitKindMeta {
  // label is the host's display name.
  label: string
  // repoPlaceholder is a sample repository URL for that host; gitlab
  // allows nested group paths, so its sample carries a subgroup.
  repoPlaceholder: string
  // sshKeysURL is the host's page for registering a public key.
  sshKeysURL: string
  // ownerLabel names the host's container for new repositories.
  ownerLabel: string
}

export const GIT_KIND_META: Record<GitKind, GitKindMeta> = {
  github: {
    label: 'GitHub',
    repoPlaceholder: 'https://github.com/owner/repo',
    sshKeysURL: 'https://github.com/settings/ssh/new',
    ownerLabel: 'Owner',
  },
  bitbucket: {
    label: 'Bitbucket',
    repoPlaceholder: 'https://bitbucket.org/workspace/repo',
    sshKeysURL: 'https://bitbucket.org/account/settings/ssh-keys/',
    ownerLabel: 'Workspace',
  },
  gitlab: {
    label: 'GitLab',
    repoPlaceholder: 'https://gitlab.com/group/subgroup/repo',
    sshKeysURL: 'https://gitlab.com/-/user_settings/ssh_keys',
    ownerLabel: 'Namespace',
  },
}

// gitKindMeta resolves a kind's copy, falling back to GitHub's for a
// kind that predates this table so callers never render blank.
export function gitKindMeta(kind: string): GitKindMeta {
  return isGitKind(kind) ? GIT_KIND_META[kind] : GIT_KIND_META.github
}
