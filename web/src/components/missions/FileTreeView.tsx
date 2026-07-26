import {
  ArrowDown01Icon,
  ArrowRight01Icon,
  FileCodeIcon,
  File02Icon,
  Folder02Icon,
  FolderOpenIcon,
  Image01Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useState } from 'react'
import { Badge } from '../ui/badge'
import type { FileTreeNode } from './fileTree'
import { previewKindOf } from './filePreviewKind'

function fileIconFor(path: string) {
  const kind = previewKindOf(path)
  if (kind === 'image') return Image01Icon
  if (kind === 'code' || kind === 'markdown') return FileCodeIcon
  return File02Icon
}

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
  const indent = { paddingLeft: `${depth * 14 + 8}px` }

  if (isDir) {
    return (
      <li>
        <button
          type="button"
          className="flex w-full items-center gap-1 py-1 pr-2 text-left text-xs hover:bg-muted/60"
          style={indent}
          onClick={() => setOpen((v) => !v)}
        >
          <HugeiconsIcon icon={open ? ArrowDown01Icon : ArrowRight01Icon} className="size-3 shrink-0" />
          <HugeiconsIcon icon={open ? FolderOpenIcon : Folder02Icon} className="size-3.5 shrink-0" />
          <span className="truncate">{node.name}</span>
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
  return (
    <li>
      <button
        type="button"
        className={`flex w-full items-center gap-1 py-1 pr-2 text-left text-xs hover:bg-muted/60 ${
          selected ? 'bg-muted' : ''
        }`}
        style={indent}
        onClick={() => onSelect(node)}
      >
        <span className="size-3 shrink-0" />
        <HugeiconsIcon icon={fileIconFor(node.path)} className="size-3.5 shrink-0" />
        <span className="truncate">{node.name}</span>
        {node.file?.declared && (
          <Badge variant="secondary" className="ml-auto shrink-0 px-1 py-0 text-[10px]">
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
