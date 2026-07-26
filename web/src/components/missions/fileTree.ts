import type { MissionFile } from '../../api/types'

export interface FileTreeNode {
  name: string
  path: string
  file?: MissionFile
  children: FileTreeNode[]
}

// buildFileTree turns the flat, workspace-relative path list the API
// returns into a nested tree for browsing — the backend has no
// directory concept (ListFiles only ever emits regular-file leaves),
// so this reconstruction is purely a client-side view concern.
export function buildFileTree(files: MissionFile[]): FileTreeNode[] {
  const root: FileTreeNode = { name: '', path: '', children: [] }
  for (const file of files) {
    const parts = file.path.split('/')
    let node = root
    let prefix = ''
    for (let i = 0; i < parts.length; i++) {
      const part = parts[i]
      prefix = prefix ? `${prefix}/${part}` : part
      const isLeaf = i === parts.length - 1
      let child = node.children.find((c) => c.name === part)
      if (!child) {
        child = { name: part, path: prefix, children: [] }
        node.children.push(child)
      }
      if (isLeaf) child.file = file
      node = child
    }
  }
  sortTree(root)
  return root.children
}

function sortTree(node: FileTreeNode): void {
  node.children.sort((a, b) => {
    if (!!a.file !== !!b.file) return a.file ? 1 : -1 // folders before files
    return a.name.localeCompare(b.name)
  })
  node.children.forEach(sortTree)
}
