import { ChevronDown, ChevronRight, File, FileCode, Folder, FolderOpen, Image } from 'lucide-react'
import { useState } from 'react'
import { Badge } from '../ui/badge'
import type { FileTreeNode } from './fileTree'
import { previewKindOf } from './filePreviewKind'

function FileTreeRow({
  node,
  depth,
  selectedPath,
  onSelect,
}: {
  node: FileTreeNode
  depth: number
  selectedPath?: string
  onSelect: (node: FileTreeNode) => void
}) {
  const [open, setOpen] = useState(true)
  const isDir = !node.file
  const indent = { paddingLeft: `${depth * 16 + 12}px` }

  if (isDir) {
    const Chevron = open ? ChevronDown : ChevronRight
    const FolderIcon = open ? FolderOpen : Folder
    return (
      <li>
        <button
          type="button"
          className="flex h-8 w-full items-center gap-1.5 pr-2 text-left text-sm outline-none hover:bg-muted/60 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
          style={indent}
          aria-expanded={open}
          onClick={() => setOpen((v) => !v)}
        >
          <Chevron className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
          <FolderIcon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
          <span className="truncate font-mono text-xs">{node.name}</span>
        </button>
        {open && (
          <ul>
            {node.children.map((child) => (
              <FileTreeRow
                key={child.path}
                node={child}
                depth={depth + 1}
                selectedPath={selectedPath}
                onSelect={onSelect}
              />
            ))}
          </ul>
        )}
      </li>
    )
  }

  const selected = node.path === selectedPath
  const kind = previewKindOf(node.path)
  const Icon = kind === 'image' ? Image : kind === 'code' || kind === 'markdown' ? FileCode : File
  return (
    <li>
      <button
        type="button"
        className={`flex h-8 w-full items-center gap-1.5 pr-2 text-left text-sm outline-none hover:bg-muted/60 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring ${
          selected ? 'bg-muted text-foreground' : ''
        }`}
        style={indent}
        aria-current={selected ? 'true' : undefined}
        onClick={() => onSelect(node)}
      >
        <span className="size-3.5 shrink-0" />
        <Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
        <span className="truncate font-mono text-xs">{node.name}</span>
        {node.file?.declared && (
          <Badge variant="secondary" size="sm" className="ml-auto">
            declared
          </Badge>
        )}
      </button>
    </li>
  )
}

export function FileTreeView({
  nodes,
  selectedPath,
  onSelect,
}: {
  nodes: FileTreeNode[]
  selectedPath?: string
  onSelect: (node: FileTreeNode) => void
}) {
  return (
    <ul className="py-1">
      {nodes.map((node) => (
        <FileTreeRow key={node.path} node={node} depth={0} selectedPath={selectedPath} onSelect={onSelect} />
      ))}
    </ul>
  )
}
